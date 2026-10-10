# Plan 008: web 端 exit_plan_mode gate 的记录与 TUI 一致——决定行逐条显示、按真实顺序落在 gate 调用处，review 卡跟在请求它的那行之后；live 与刷新重载一致

> **执行者须知**：按步骤顺序执行，每步跑验证命令并确认预期结果后再进下一步。触发
> "STOP conditions" 时停下报告，不要即兴发挥。完成后更新 `plans/README.md` 里本计划的状态行。
> **禁止提交代码**（owner 手动提交）：不建分支、不 commit、不 push，改动留在工作区。
>
> **Drift check（先跑）**：
> `git diff --stat 9656051 -- frontend/src/composables/useChatStream.ts frontend/src/composables/useChatStream.test.ts`
> 写计划时这两个文件与 HEAD 一致；若有差异，对照下方 "Current state" 摘录逐段核对，不一致即 STOP。
> `frontend/src/views/ChatView.vue`、`frontend/src/views/WorkshopView.vue`、`frontend/e2e/exit-plan-approval.spec.ts`
> 在写计划时已有 owner 未提交的改动（与本计划无关）——**只改本计划点名的行，不要还原或整理其他 hunk**。

## Status

- **Priority**: P2（web 端可见的顺序错误与丢行；TUI 侧 bug 由 007 修）
- **Effort**: M
- **Risk**: MED（改动 web 时间线的块模型：新增一种不画任何东西的块；有完整的 live/重载对照测试兜底）
- **Depends on**: `plans/007-exit-gate-live-card-and-cardless-anchoring.md`——**语义依赖，代码无依赖**。本计划让 web
  对齐 007 落地后的 TUI 呈现；两者改的文件不重叠，可以并行执行，但应一起交付，否则两端仍不一致。
- **Category**: bug
- **Planned at**: commit `9656051`，2026-10-10

## Why this matters

owner 的常设规则：**TUI 的语义是标准，web 必须统一到 TUI。** 007 之后，TUI 对 exit_plan_mode gate
（"无卡 gate"——等待期间不画卡）的呈现是：

```
● <发出 exit_plan_mode 的那段回答>
✔ You asked zhipuai / glm-5.3 to review the plan      ← 用户选 review 时打印的行
○ Running 1 plan-reviewer task …                      ← review 卡，紧跟在请求它的行之后
✔ You approved forebrain to exit plan mode            ← 同一个 gate 的后续决定（如 review 失败后批准）
◆ Exited plan mode                                    ← settled 卡（批准/拒绝/失败才有）
<之后的内容>
```

取消时只有 `✗ You canceled …` 一行，紧跟在那段回答之后；review 交付后模型修订的内容出现在 review 卡**之后**。

web 今天有三处与之不符（全部由现有测试钉死，见 Current state）：

1. **落位错误**：无卡 gate 的审批行找不到对应的卡，退回"插到本轮最后一张工具卡之前"——"✗ You canceled …"
   会跑到更早的 shell 卡、写计划文件的卡**之上**，live 和重载都这样。
2. **丢行**：web 一个 action 只保留一个审批块，新决定覆盖旧块的 `confirmation`。同一 gate 先 "You asked … review"
   再 "You approved …" 时只剩后一行；review 交付（不带行）会把 "You asked …" **整行抹掉**——用户看过的话从页面上消失。
3. **review 卡落位**：重载时 review 卡被追加到整轮回答的**末尾**；review 交付后同一轮还有修订内容和新的 gate，
   卡片就跑到了这些内容之后。

## 根因与证据

- web 的时间线：一轮回答是一个 `ChatMessage`，`blocks` 是按发生顺序排列的块。live 时块随事件追加；
  重载时 `conversationFromTranscript` 按 transcript 行重建块，再按 sequence 回放事件。
- exit gate 的等待帧被 `stepHoldsNoCard` 跳过（`useChatStream.ts:3411-3413`），所以时间线里**没有任何东西标记
  gate 调用的位置**。重载时只用 `ChatMessage.gateStepIds`（`:114-119`、`:657-660`）记录"这一轮里有个无卡 gate"，
  没有位置。
- 审批块落位 `upsertApprovalBlock`（`:869-889`）：找到被审批的卡就插在卡之上；找不到就插在最后一张工具卡之前
  （注释明说是为了和 TUI 一致——那是 007 之前 TUI 的错位）。
- 同一 action 只有一个块：`upsertApprovalBlock` 按 `actionId` 合并 `{ ...block, ...next }`，`next.confirmation`
  为 `undefined` 时会覆盖掉已有的行（`:873-877`；`approval_resolved` 处 `:3929-3950` 传入的 `confirmation`
  在无行时就是 `undefined`）。对比 subagent 路径 `:2914` 用的是 `confirmation ?? block.confirmation`，不会抹行。
- review 卡：`plan_review_started`（`:3579-3597`）调 `upsertSubagentExecutionBlock`（`:2228-2250`），后者把块
  **追加**到 host 消息末尾；重载时 host 是该 run 的那一轮，事件在 transcript 之后回放，所以落在整轮末尾。
- 运行时事件形态（已核实）：`approval_requested` / `approval_resolved` 的 payload 带 `actionKind`（exit gate 为
  `"exit_plan_mode"`）和 `toolStepId`（被审批的调用 id）；TUI 发起的 review 会先写一条
  `approval_resolved{decision:"review_requested", confirmation:"✔ You asked …"}`，web 发起的 review 不写任何行；
  `plan_review_started` 的 payload 带 `actionId` 和 `reviewId`（`pkg/event/run_events.go:396-402`）；
  review 交付是一条不带可显示行的 `approval_resolved{decision:"denied"}`；运行时对 deny 不发工具步骤事件。

## 修复设计

**给无卡 gate 一个不画任何东西的"锚点块"，标记它的调用在时间线里的位置；这个 gate 的所有记录（每条决定行、
它要求的 review 卡）都按到达顺序插在锚点之前。** settled 卡（批准/拒绝/失败）照旧画在锚点之后。

- 新块类型：`{ kind: 'gate'; id: string; stepId: string; actionId?: string }`。渲染层（`ChatView.vue`、
  `WorkshopView.vue`）的 `v-if/v-else-if` 链没有兜底分支，未知块天然不渲染——无需加渲染分支。
- **live**：exit gate 的 started / awaiting 步骤不再直接 `return`，而是确保锚点存在（不存在就追加在当前回答末尾，
  并先关闭正在流式的 assistant/thinking 块——与工具卡到达时的行为一致）。
- **重载**：`conversationFromTranscript` 遇到 exit_plan_mode 调用时**总是**先放锚点，再按答复决定是否画 settled 卡
  （canceled / review 交付 / 无答复 → 只有锚点）。删除 `gateStepIds`。
- **审批事件（仅 exit gate，`actionKind === 'exit_plan_mode'`）**：`approval_requested` 只确保锚点存在并记下
  `actionId`，不再生成审批块；`approval_resolved` 同样确保锚点，**有可显示的行时**生成一个独立的审批块
  （id 取事件 id），插在锚点之前；无行（web 端做的决定、review 交付）不画任何东西。非 exit gate 的审批**完全不变**。
- **review 卡**：`plan_review_started` 先按 `actionId` 找锚点，找到就插在锚点之前；找不到（旧数据没有
  `approval_requested`）退回原来的追加行为。

推演（live 与重载结果相同）：

| 场景 | 结果块序 |
|---|---|
| 取消 | `assistant` → `approval(✗ You canceled)` → `gate` |
| review 交付后修订并批准新 gate | `assistant(A)` → `approval(You asked)` → `subagent(review)` → `gate(A)` → `assistant(修订)` → `approval(You approved)` → `gate(B)` → `tool(B, Exited plan mode)` |
| review 失败后批准同一 gate | `assistant` → `approval(You asked)` → `subagent(review)` → `approval(You approved)` → `gate` → `tool(Exited plan mode)` |
| 用户拒绝（带反馈） | `assistant` → `approval(✗ You did not approve)` → `gate` → `tool(Kept planning)` |
| web 上做的决定（事件不带行） | `assistant` → `gate` →（批准/拒绝时）`tool` |

## Current state（摘录，HEAD 9656051）

`frontend/src/composables/useChatStream.ts:114-120`（`ChatMessage` 末尾字段，删除）：

```ts
  /**
   * Tool steps this turn's transcript carried whose cards are withheld — the
   * exit-plan gate holds no card on any surface. An approval record naming
   * one of these anchors to this turn: the decision it carries is history
   * even though the wait it held drew nothing.
   */
  gateStepIds?: string[]
}
```

`:464-480`（exit gate 判定，保留）：

```ts
function isExitPlanGate(toolName: string): boolean {
  return String(toolName ?? '').trim().toLowerCase() === 'exit_plan_mode'
}
function stepHoldsNoCard(step: { toolName: string; status: string }): boolean {
  return isExitPlanGate(step.toolName) && ['running', 'awaiting approval', 'canceled'].includes(String(step.status ?? '').trim().toLowerCase())
}
```

`:643-672`（`conversationFromTranscript` 里 assistant 行的调用循环）：

```ts
    for (const call of parseStoredToolCalls(row.partsJson)) {
      if (drawn.has(call.id)) continue
      const answer = toolRowByCall.get(call.id)
      // A delivered review closed this gate: ...
      if (answer && isPlanReviewDeliveredRow(answer)) continue
      if (answer && isCanceledExitRow(answer)) continue
      // The exit-plan gate holds no card while it waits, ...
      if (!answer && isExitPlanGate(String(call.name ?? ''))) {
        current.gateStepIds = [...(current.gateStepIds ?? []), String(call.id ?? '').trim()].filter(Boolean)
        continue
      }
      drawn.add(call.id)
      current.blocks = [...(current.blocks ?? []), {
        kind: 'tool',
        step: answer ? toolStepFromRow(answer, call) : canceledToolStep(call),
      }]
    }
```

（同函数 `role === 'tool'` 分支 `:615-630` 对"没有 assistant 行认领的 tool 行"也做了 delivered/canceled 丢弃——**保留不动**。）

`:869-889` `upsertApprovalBlock`（非 exit gate 继续使用；只改注释）：

```ts
export function upsertApprovalBlock(blocks, next) {
  const index = blocks.findIndex((block) => block.kind === 'approval' && block.actionId === next.actionId)
  if (index >= 0) {
    return blocks.map((block, i) => (
      i === index && block.kind === 'approval' ? { ...block, ...next, id: block.id || next.id } : block
    ))
  }
  const gated = next.toolStepId
    ? blocks.findIndex((block) => block.kind === 'tool' && block.step.stepId === next.toolStepId)
    : -1
  if (gated >= 0) return [...blocks.slice(0, gated), next, ...blocks.slice(gated)]
  // A gate whose own card is withheld - the exit-plan gate holds no card -
  // still lands above the calls of the turn it belongs to, the same place the
  // terminal prints the line for it.
  const lastTool = blocks.map((block) => block.kind === 'tool').lastIndexOf(true)
  if (lastTool >= 0) return [...blocks.slice(0, lastTool), next, ...blocks.slice(lastTool)]
  return [...blocks, next]
}
```

`:904-950` `TimelineBlock` 联合类型（在末尾 `| { kind: 'subagent'; … }` 之后加新成员）。

`:1055-1070` `closeStreamedBlocks(blocks, kinds, occurredAt?)`——关闭 open 的 assistant/thinking 块（模块级函数）。

`:2228-2250` `upsertSubagentExecutionBlock(evt, blockId, step, fallbackAssistantMessageId?)`——把 `subagent` 块追加到 host 末尾。

`:3350-3388`（composable 内）`updateMessageById`、`upsertConversationApproval`：

```ts
  function upsertConversationApproval(assistantMessageId, toolStepId, block) {
    const owner = toolStepId
      ? messages.value.find((message) => (message.blocks ?? []).some(
        (existing) => existing.kind === 'tool' && existing.step.stepId === toolStepId,
      ) || message.gateStepIds?.includes(toolStepId))
      : undefined
    ...
```

`:3390-3426` `handleStepEvent`：

```ts
    // The exit-plan gate's wait paints no card: its approval prompt is the
    // whole wait (stepHoldsNoCard).
    if (stepHoldsNoCard(step)) return
    rememberSubagentCallStep(step)
    updateMessageById(assistantMessageId, (message) => ({
      ...message,
      blocks: upsertToolBlock(
        closeStreamedBlocks(message.blocks ?? [], ['assistant', 'thinking']),
        step,
        evt.id,
      ),
    }))
```

`:3579-3597` `plan_review_started`：

```ts
    if (evt.type === 'plan_review_started') {
      const reviewId = String(payload.reviewId ?? '').trim()
      if (!reviewId) return
      const provider = ...; const model = ...; const label = ...
      upsertSubagentExecutionBlock(evt, `plan-review:${reviewId}`, {
        stepId: reviewId, toolName: '', summary: …, status: 'running',
        subagentCall: { verb: 'review', tasks: [{ index: 0, title: 'Plan review', agentType: 'plan-reviewer', status: 'waiting' }] },
      }, assistantMessageId)
      subagentCallStepIds.add(reviewId)
      return
    }
```

`:3911-3950` `handleRunEvent` 的 `approval_requested` / `approval_resolved`（conversation 分支，均调
`upsertConversationApproval(assistantMessageId, toolStepId, {kind:'approval', id:`approval-${actionId}`, …})`；
`approval_resolved` 的 `confirmation` 为 `confirmationRaw && confirmationRaw !== PLAN_REVIEW_DELIVERED_KEY ? confirmationRaw : undefined`）。

`frontend/src/views/ChatView.vue:1501-1512` `messageHasBubble`：`msg.blocks?.length || …` 即算有内容。
`frontend/src/views/WorkshopView.vue:39-41`：`v-if="message.role === 'assistant' && message.blocks?.length"`。

### 现有测试里钉死旧行为的用例（`frontend/src/composables/useChatStream.test.ts`）

| 行 | 用例 | 现期望 | 新期望 |
|---|---|---|---|
| ~2030 | turns a conversation approval into a block and stamps the printed line on it | requested 后 `blocks[0]` 是 pending 审批块；resolved 后只有 1 个块 | requested 后只有 `gate`（带 `actionId`）；resolved 后 `['approval','gate']` |
| ~2058 | closes a delivered plan review card silently, with no line | 有一个 `status:'denied'`、无行的审批块 | 没有任何审批块，只有 `gate` |
| ~2108 | drops a canceled gate row whole and still shows the confirmation line | `['assistant','approval']` | `['assistant','approval','gate']` |
| ~2150 | keeps a denied gate row card from history | `['assistant','tool']` | `['assistant','gate','tool']` |
| ~2225-2335 | reloads a turn into the same timeline the live stream built | 第二轮块序 `['thinking','assistant','approval','tool','assistant']` | `['thinking','assistant','tool','assistant','approval','gate']`（live 与重载仍须相等） |
| ~2880-2915 | lands a replayed approval directly above the call it gated | `['approval','tool']`，行在后发出的 shell 卡之上 | `['approval','gate','tool']`：行在 exit 调用自己的位置 |

### 仓库约定

- 注释写"为什么"、英文完整句子，风格与上面摘录一致；新导出函数带 doc 注释。
- 被删掉的语义不留反向断言（owner 既有裁决）：旧用例按新契约改写，不保留"断言旧行为不再出现"的考古用例。
- 测试辅助：`withFakeWebSocket` + `startLiveStream` + `runEvent(socket, id, seq, runId, sessionId, type, payload)` +
  `endTurn`（`:1872-1920`）。**注意**：这个 `runEvent` 的 `created_at` 是 `` `2026-06-14T00:00:0${sequence}.000Z` ``，
  sequence ≥ 10 会生成非法时间——一个用例里事件超过 9 条时改用 `liveStream()`（`:3279-3303`，自增 id/sequence、固定时间，
  run 为 `run-1`、session 为 `s1`）。重载用例照 `:2108-2148` 用 `vi.spyOn(forebrainApi, 'chatMessages' | 'sessionEvents' | 'sessionSubagentHistory')` 打桩。

## Commands you will need

| 用途 | 命令（在 `frontend/` 下） | 成功预期 |
|---|---|---|
| 单文件测试 | `npx vitest run src/composables/useChatStream.test.ts` | 全部通过（基线 102 passed） |
| 全部测试 | `pnpm test` | 全部通过 |
| 类型检查 | `npx vue-tsc --noEmit -p tsconfig.json` | exit 0、无输出（基线即如此） |
| 构建 | `pnpm build` | 成功（会重写 `pkg/gateway/dist`，属预期产物） |

## Scope

**In scope**：
- `frontend/src/composables/useChatStream.ts`
- `frontend/src/composables/useChatStream.test.ts`
- `frontend/src/views/ChatView.vue`——只改 `messageHasBubble` 一行与 import
- `frontend/src/views/WorkshopView.vue`——只改 assistant 块容器的 `v-if` 条件与 import
- 构建产物 `pkg/gateway/dist`（`pnpm build` 生成）

**Out of scope**：
- **非 exit gate 的审批**（shell、request_permissions、enter_plan_mode……）与 subagent 视图里的审批
  （`routeSubagentEvent` 的 `approval_requested/resolved`，`:2874-2930`）——行为一律不变。
- `upsertApprovalBlock` 的逻辑（只改注释删掉提到 exit gate 的那句）。
- `ApprovalCard.vue`（它只渲染 `confirmation`，新块形状与之兼容）、`PendingActionsPanel`、e2e 用例。
- 任何 Go 代码（TUI 侧由 007 负责；运行时事件形态不变）。
- `ChatView.vue` 里"打字中"三点的条件（`!msg.blocks?.length`）——锚点存在时（gate 停靠等待审批）不显示三点，是可接受的；不改。

## Git workflow

不建分支、不 commit、不 push（owner 手动提交）。改动留在工作区。

## Steps

### Step 1：新增锚点块类型与纯函数

`useChatStream.ts`：

1. `TimelineBlock` 联合类型末尾加：

```ts
  /**
   * Where a call that holds no card sits on the timeline — the exit-plan gate,
   * whose wait draws nothing. It draws nothing either: it is the position the
   * gate's records (each line the terminal printed for a decision on it, the
   * plan review it asked for) are placed at, in the order they arrived, so a
   * live turn, a reloaded one and the terminal all read the same.
   */
  | { kind: 'gate'; id: string; stepId: string; actionId?: string }
```

2. 在 `upsertApprovalBlock` 附近新增并导出：

```ts
/** gateAnchorId is the identity of a card-less call's anchor. */
export function gateAnchorId(stepId: string, actionId = ''): string {
  return stepId ? `gate:${stepId}` : `gate-action:${actionId}`
}

/**
 * placeBeforeGateAnchor puts one of a card-less gate's records directly above
 * its anchor, after the records already there, so they read in the order they
 * arrived. A record already on the timeline is updated where it stands. It
 * returns null when these blocks hold no anchor for the gate. The anchor is
 * matched by the call it names or, failing that, the action that asked.
 */
export function placeBeforeGateAnchor(
  blocks: TimelineBlock[],
  stepId: string,
  actionId: string,
  next: Extract<TimelineBlock, { id: string }>,
): TimelineBlock[] | null {
  const existing = blocks.findIndex((block) => block.kind === next.kind && block.id === next.id)
  if (existing >= 0) return blocks.map((block, i) => (i === existing ? { ...block, ...next } as TimelineBlock : block))
  const anchor = blocks.findIndex((block) => block.kind === 'gate' && (
    (stepId !== '' && block.stepId === stepId) || (actionId !== '' && block.actionId === actionId)
  ))
  if (anchor < 0) return null
  return [...blocks.slice(0, anchor), next, ...blocks.slice(anchor)]
}

/**
 * hasVisibleBlocks reports whether a timeline draws anything: a gate anchor
 * only marks where a card-less call sits.
 */
export function hasVisibleBlocks(blocks?: TimelineBlock[]): boolean {
  return (blocks ?? []).some((block) => block.kind !== 'gate')
}
```

（若 `Extract<TimelineBlock, { id: string }>` 因某些成员 `id?` 可选而推导不出想要的类型，改用
`TimelineBlock & { id: string }`；以 `vue-tsc` 通过为准。）

**Verify**：`cd frontend && npx vue-tsc --noEmit -p tsconfig.json` → exit 0、无输出。

### Step 2：重载——transcript 里每个 exit gate 调用都放锚点，删除 `gateStepIds`

1. 删除 `ChatMessage.gateStepIds` 字段及其 doc（`:114-119`）。
2. `conversationFromTranscript` 的调用循环（`:643-672`）改为：

```ts
    for (const call of parseStoredToolCalls(row.partsJson)) {
      if (drawn.has(call.id)) continue
      const answer = toolRowByCall.get(call.id)
      // The exit-plan gate holds no card while it waits, and none once a run
      // stopped on it or a delivered review closed it: only its settled answers
      // draw. Its anchor always marks where the call sits, because the records
      // about it — the lines its decisions printed, the review it asked for —
      // belong right there, in the order they happened.
      if (isExitPlanGate(String(call.name ?? ''))) {
        drawn.add(call.id)
        current.blocks = [...(current.blocks ?? []), { kind: 'gate', id: gateAnchorId(call.id), stepId: call.id }]
        if (answer && !isPlanReviewDeliveredRow(answer) && !isCanceledExitRow(answer)) {
          current.blocks = [...current.blocks, { kind: 'tool', step: toolStepFromRow(answer, call) }]
        }
        continue
      }
      drawn.add(call.id)
      current.blocks = [...(current.blocks ?? []), {
        kind: 'tool',
        // A call with no tool row never ran: ... （保留原注释）
        step: answer ? toolStepFromRow(answer, call) : canceledToolStep(call),
      }]
    }
```

   （原先循环里的两行 `isPlanReviewDeliveredRow` / `isCanceledExitRow` 判断只可能命中 exit gate，已并入上面的分支，删去；
   `role === 'tool'` 分支里的同名判断**保留**。）
3. 更新 `conversationFromTranscript` 上方 doc 中涉及 gate 的描述（若有），与新行为一致。

**Verify**：`npx vue-tsc --noEmit -p tsconfig.json` → 只剩 `gateStepIds` 在 `upsertConversationApproval` 里的引用报错
（Step 3 修掉）；`grep -n 'gateStepIds' src/composables/useChatStream.ts` → 只剩那一处。

### Step 3：live 与事件——锚点的建立与记录的落位

在 composable 内（`updateMessageById` 附近）新增：

```ts
  /**
   * findGateAnchor names the message holding a card-less gate's anchor.
   */
  function findGateAnchor(stepId: string, actionId: string): string | null {
    for (const message of messages.value) {
      const holds = (message.blocks ?? []).some((block) => block.kind === 'gate' && (
        (stepId !== '' && block.stepId === stepId) || (actionId !== '' && block.actionId === actionId)
      ))
      if (holds && message.id) return message.id
    }
    return null
  }

  /**
   * ensureGateAnchor makes sure a card-less gate has its anchor and knows the
   * action asking about it. A gate first seen here — its call's own step
   * never arrived — is anchored at the end of the answer it belongs to,
   * which is where the call was made: the run is parked on it.
   */
  function ensureGateAnchor(hostId: string, stepId: string, actionId: string) {
    const owner = findGateAnchor(stepId, actionId)
    if (owner) {
      if (actionId) {
        updateMessageById(owner, (message) => ({
          ...message,
          blocks: (message.blocks ?? []).map((block) => (
            block.kind === 'gate' && ((stepId !== '' && block.stepId === stepId) || block.actionId === actionId)
              ? { ...block, actionId }
              : block
          )),
        }))
      }
      return
    }
    if (!hostId) return
    updateMessageById(hostId, (message) => ({
      ...message,
      blocks: [
        ...closeStreamedBlocks(message.blocks ?? [], ['assistant', 'thinking']),
        { kind: 'gate', id: gateAnchorId(stepId, actionId), stepId, ...(actionId ? { actionId } : {}) },
      ],
    }))
  }

  /**
   * placeBeforeGate puts a record of a card-less gate above its anchor,
   * wherever that anchor is. False when no anchor names the gate.
   */
  function placeBeforeGate(stepId: string, actionId: string, block: Extract<TimelineBlock, { id: string }>): boolean {
    const owner = findGateAnchor(stepId, actionId)
    if (!owner) return false
    updateMessageById(owner, (message) => ({
      ...message,
      blocks: placeBeforeGateAnchor(message.blocks ?? [], stepId, actionId, block) ?? message.blocks,
    }))
    return true
  }
```

然后：

1. `handleStepEvent`（`:3411-3413`）：把 `if (stepHoldsNoCard(step)) return` 改为

```ts
    // The exit-plan gate's wait paints no card: its approval prompt is the
    // whole wait (stepHoldsNoCard). Its anchor still marks where the call was
    // made, so the gate's records land there.
    if (stepHoldsNoCard(step)) {
      ensureGateAnchor(assistantMessageId, step.stepId, '')
      return
    }
```

2. `handleRunEvent` 的 conversation `approval_requested`（`:3911-3927`）：在 `if (!actionId) return` 之后加

```ts
        // The exit-plan gate holds no card and its pending record draws
        // nothing: the wait is its anchor, which learns the action asking.
        if (isExitPlanGate(String(payload.actionKind ?? ''))) {
          ensureGateAnchor(assistantMessageId, String(payload.toolStepId ?? '').trim(), actionId)
          return
        }
```

3. conversation `approval_resolved`（`:3929-3950`）：在 `if (!actionId) return` 之后、计算出 `confirmationRaw` 之后加

```ts
        // Each decision on the exit-plan gate is its own line, exactly as the
        // terminal printed it — a review asked for and the approval that came
        // after are two lines, and a later decision never takes an earlier
        // line away. A decision that printed nothing draws nothing.
        if (isExitPlanGate(String(payload.actionKind ?? ''))) {
          const toolStepId = String(payload.toolStepId ?? '').trim()
          ensureGateAnchor(assistantMessageId, toolStepId, actionId)
          const decision = normalizeApprovalDecision(String(payload.decision ?? payload.status ?? 'resolved').trim())
          if (confirmationRaw && confirmationRaw !== PLAN_REVIEW_DELIVERED_KEY) {
            placeBeforeGate(toolStepId, actionId, {
              kind: 'approval',
              id: String(evt.id ?? '').trim() || `approval-${actionId}-${decision}`,
              actionId,
              actionKind: String(payload.actionKind ?? '').trim(),
              status: decision,
              confirmation: confirmationRaw,
              message: String(payload.reason ?? '').trim() || undefined,
              toolStepId: toolStepId || undefined,
            })
          }
          return
        }
```

4. `plan_review_started`（`:3579-3597`）：把构造好的 step 存到局部变量，替换 `upsertSubagentExecutionBlock(...)` 调用为

```ts
      const blockId = `plan-review:${reviewId}`
      const actionId = String(payload.actionId ?? '').trim()
      const drawn = messages.value.some((message) => (message.blocks ?? []).some((b) => b.kind === 'subagent' && b.id === blockId))
      // A review asked from the exit-plan gate belongs to that gate's
      // exchange: right after the line that asked for it, where the
      // terminal draws it. A review whose gate this client never saw keeps
      // its place at the end of the answer.
      if (drawn || !actionId || !placeBeforeGate('', actionId, { kind: 'subagent', id: blockId, stepId: reviewId, step })) {
        upsertSubagentExecutionBlock(evt, blockId, step, assistantMessageId)
      }
```

5. `upsertConversationApproval`（`:3367-3388`）：owner 查找去掉 `|| message.gateStepIds?.includes(toolStepId)`。
6. `upsertApprovalBlock` 注释删掉 "A gate whose own card is withheld - the exit-plan gate holds no card - still lands above
   the calls of the turn it belongs to …" 这三行，改为说明该回退只服务**找不到被审批卡的非 exit gate 记录**
   （exit gate 的记录走锚点，不进这里）。

**Verify**：`npx vue-tsc --noEmit -p tsconfig.json` → exit 0、无输出；`grep -n 'gateStepIds' src/composables/useChatStream.ts` → 无输出。

### Step 4：视图——只含锚点的回答不画空气泡

1. `ChatView.vue`：import 里加 `hasVisibleBlocks`（`:651` 那行 import）；`messageHasBubble`（`:1501-1512`）里
   `msg.blocks?.length ||` 改为 `hasVisibleBlocks(msg.blocks) ||`。
2. `WorkshopView.vue`：import 加 `hasVisibleBlocks`（`:160`）；`:39-41` 的
   `v-if="message.role === 'assistant' && message.blocks?.length"` 改为 `v-if="message.role === 'assistant' && hasVisibleBlocks(message.blocks)"`。
3. 两个文件其他行（owner 未提交的改动）不碰。

**Verify**：`npx vue-tsc --noEmit -p tsconfig.json` → exit 0；`git diff frontend/src/views/ChatView.vue frontend/src/views/WorkshopView.vue | grep '^[-+][^-+]' | grep -c 'hasVisibleBlocks'` ≥ 4（两处 import、两处使用）。

### Step 5：测试（改写 + 新增）

见 "Test plan"。

**Verify**：`npx vitest run src/composables/useChatStream.test.ts` → 全部通过。

### Step 6：全量

```bash
cd frontend
pnpm test
npx vue-tsc --noEmit -p tsconfig.json
pnpm build
```

**Verify**：测试全绿；类型检查无输出；构建成功。

## Test plan

所有用例放在 `frontend/src/composables/useChatStream.test.ts`。

**改写**（见 Current state 表格；每个用例的 doc 注释按新语义重写）：

1. `turns a conversation approval into a block …`（~2030）→ 改名 `anchors an exit-gate approval and stamps each printed line above it`：
   requested 后 host 只有 `{kind:'gate', stepId:'call-exit', actionId:'act-1'}`；resolved 后 `['approval','gate']`，
   审批块 `{actionId:'act-1', status:'cancelled', confirmation:"✗ You canceled …"}`。
2. `closes a delivered plan review card silently, with no line`（~2058）：断言 host 里**没有** `approval` 块、只有 `gate`。
3. `drops a canceled gate row whole …`（~2108）：块序 `['assistant','approval','gate']`，`blocks[1]` 断言不变。
4. `keeps a denied gate row card from history`（~2150）：块序 `['assistant','gate','tool']`，`blocks[2]` 为 denied 卡。
5. 对照用例 `reloads a turn into the same timeline the live stream built`（~2225-2335）：第二轮块序改为
   `['thinking','assistant','tool','assistant','approval','gate']`，`timeline(reloaded) === timeline(live)` 的断言保留；
   更新那段注释（行落在本轮 exit 调用自己的位置，而不是更早的 shell 卡之上）。
6. `lands a replayed approval directly above the call it gated`（~2880-2915）：块序 `['approval','gate','tool']`，
   注释改为"行落在 exit 调用自己的位置；之后发出的 shell 调用在它下面"。

**新增**：

7. **纯函数** `placeBeforeGateAnchor`（导出后加入文件顶部 import）：①锚点前已有一条记录时，新记录插在那条之后、锚点之前；
   ②同 `kind`+`id` 的块原地更新、不新增；③按 `actionId` 也能找到锚点；④没有锚点返回 `null`。`hasVisibleBlocks`：
   只有 `gate` → false；含 `assistant` → true。
8. **重载画锚点**：`conversationFromTranscript` 对 exit 调用——无答复 / canceled 行 / review 交付行 → 该位置只有 `gate`；
   denied / completed 行 → `gate` 紧跟 `tool`；非 exit 调用（shell）不受影响。（样板：`draws nothing for a delivered gate …` ~2086。）
9. **live：review 交付后修订并批准新 gate**（`exit gate records follow the terminal through a delivered review`；事件多于 9 条，用 `liveStream()`）：
   依次发 `assistant_delta('here is the plan')`、`tool_call_started{toolName:'exit_plan_mode', stepId:'call-a', toolMeta:{tool_name:'exit_plan_mode', status:'running'}}`、
   `approval_requested{actionId:'act-a', actionKind:'exit_plan_mode', toolStepId:'call-a'}`、
   `approval_resolved{actionId:'act-a', actionKind:'exit_plan_mode', decision:'review_requested', toolStepId:'call-a', confirmation:'✔ You asked zhipuai/glm-5.3-flash to review the plan'}`、
   `plan_review_started{action_id:'act-a', review_id:'rev-1', provider:'zhipuai', model:'glm-5.3-flash'}`、
   `plan_reviewed{action_id:'act-a', review_id:'rev-1', outcome:'done', text:'tighten step 2'}`、
   `approval_resolved{actionId:'act-a', actionKind:'exit_plan_mode', decision:'denied', toolStepId:'call-a', reason:'plan-review:delivered'}`、
   `assistant_delta('revised plan')`、`tool_call_started{… stepId:'call-b' …}`、`approval_requested{actionId:'act-b', …, toolStepId:'call-b'}`、
   `approval_resolved{actionId:'act-b', …, decision:'approved', toolStepId:'call-b', confirmation:'✔ You approved forebrain to exit plan mode'}`、
   `tool_call_completed{toolName:'exit_plan_mode', stepId:'call-b', displayBody:'Exited plan mode.', toolMeta:{tool_name:'exit_plan_mode', status:'completed'}}`。
   断言 host 块序：`['assistant','approval','subagent','gate','assistant','approval','gate','tool']`；第一个审批块的
   `confirmation` 仍是 "You asked …"（没有被交付抹掉）；`subagent` 块的 id 为 `plan-review:rev-1`。
   （`liveStream()` 的 payload 用 snake_case 还是 camelCase 都可以——照该辅助既有用例的写法，如 `:3481` 用 snake_case。）
10. **重载对照 9**（`a reloaded delivered review reads like the live one`）：用 transcript 行
    （user → assistant `here is the plan` + 调用 `call-a` → tool 行 `call-a`，display body 为 `PLAN_REVIEW_DELIVERED_KEY`、
    meta `{tool_name:'exit_plan_mode', status:'denied'}` → assistant `revised plan` + 调用 `call-b` → tool 行 `call-b`，
    body `'Exited plan mode.'`、meta status `completed`）+ 与 9 同序的事件（去掉 assistant_delta 与 tool_call_* 这类
    transcript 已承载的事件）经 `loadMessages` 重载；断言该轮块序与 9 完全相同（含 `'subagent'` 的位置）。
11. **review 失败后批准同一 gate**（`two decisions on one gate are two lines, the review between them`，live 与重载各一遍）：
    同一个 `act-1`/`call-exit` 上依次 `review_requested`（带行）→ `plan_review_started` → `plan_reviewed{outcome:'failed', error:'no plan to review'}` →
    `approved`（带行）→ completed 工具步骤；块序 `['assistant','approval','subagent','approval','gate','tool']`，两个审批块的 `confirmation` 依次为
    "You asked …"、"You approved …"。
12. **非 exit gate 不变**：shell 审批（`actionKind:'shell'`，toolStepId 指向已画出的 shell 卡）仍是一个按 `actionId` 合并的审批块、插在该卡之上
    （与既有用例 `keeps a gate whose call this client cannot place on its run projection` ~2918 一起保证非 exit 路径没被波及）。

**Verify**：`npx vitest run src/composables/useChatStream.test.ts` → 全部通过（102 − 0 + 新增数，改写的仍计在内）。

## Done criteria

- [ ] `cd frontend && pnpm test` 全绿，含上述 6 个改写与 6 组新增用例
- [ ] `npx vue-tsc --noEmit -p tsconfig.json` exit 0、无输出
- [ ] `pnpm build` 成功
- [ ] `grep -rn 'gateStepIds' frontend/src` 无输出
- [ ] `upsertApprovalBlock` 注释里不再提 exit gate：`grep -n 'the exit-plan gate holds no card -' frontend/src/composables/useChatStream.ts` 无输出
- [ ] `git status` 中本计划的改动只涉及 Scope 列出的文件（owner 既有未提交改动与 `pkg/gateway/dist` 构建产物除外）
- [ ] `plans/README.md` 中 008 状态更新；若 007 已完成，在本计划末尾"验收记录"里写一段与 TUI 的对照
      （同一会话 TUI 回放与 web 重载的块序一致，可用 007 Step 8 的 driver 会话 + `forebrain gateway` 打开同一会话核对，
      或注明交 owner 手动核对）

## STOP conditions

1. Drift check 发现 `useChatStream.ts` / `useChatStream.test.ts` 与摘录不一致。
2. 改写用例 5（live/重载对照）出现 live 与重载块序不相等——说明某条事件在两条路径上的处理不同，停下报告差异，不要为了让测试过而改断言。
3. 任何**非 exit gate** 的审批用例变红，或修改需要改变 `upsertApprovalBlock` 的逻辑。
4. 发现除 `ChatView.vue`、`WorkshopView.vue`、`SubagentConversation.vue` 之外还有组件按 `block.kind` 渲染会话块且带兜底分支
   （`v-else` 无条件分支会把 `gate` 当成别的东西画出来）——`grep -rn "block.kind" frontend/src --include=*.vue` 核对后停下报告。
5. 需要改 Go 代码或运行时事件形态才能完成某一步。

## Maintenance notes

- **"无卡 gate"在 web 有三处定义，改一处要同步其余**：`isExitPlanGate` / `stepHoldsNoCard`（哪些步骤不画卡）、
  `conversationFromTranscript` 的 exit 分支（重载放锚点）、`handleRunEvent` 的 exit 审批分支（记录落位）。它们对应运行时的
  `tool.ToolStepHoldsNoCard` 与 007 新增的 TUI `approvalGateHoldsNoCard`。将来若别的 gate 也改成无卡，三处一起扩。
- 锚点块不渲染任何东西；新增按 `block.kind` 渲染的组件时**不要**加无条件 `v-else` 兜底，否则锚点会被画出来。
  需要判断"一轮回答有没有可见内容"时用 `hasVisibleBlocks`，不要用 `blocks.length`。
- exit gate 的审批块现在**一条决定一个块**（id 取事件 id），其余 gate 仍是**一个 action 一个块**。若将来统一，
  优先让其他 gate 也改成逐条，而不是回到合并（合并会丢掉终端打印过的行）。
- 没有 `toolStepId` 的旧记录：锚点按 `actionId` 建在该轮回答末尾，表现与修复前接近；没有 `approval_requested` 的旧会话，
  review 卡退回追加到末尾。
- 评审重点：`ensureGateAnchor` 只在首次建锚时关闭流式块；`placeBeforeGateAnchor` 的幂等（同一事件经请求 socket 与会话观察者
  两路到达时由 `appliedEventIDs` 去重，这里再兜一层）。
