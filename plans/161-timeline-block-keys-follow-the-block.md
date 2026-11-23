# Plan 161：时间线块的 key 跟着块走，不跟位置走

> **执行者须知**：按步骤顺序执行，每步跑验证命令并确认预期结果后再进下一步。触发
> "STOP conditions" 时停下报告，不要即兴发挥。完成后把 `plans/README.md` 里本计划的状态行改为 DONE
> （若评审方派发你并说明由其维护索引，则不改）。
> **禁止提交代码**（owner 手动提交）：不 commit、不 push、不开 PR、不建分支，改动直接留在工作区。
> 工作区里与本计划无关的未提交改动**不要碰、不要回滚、不要格式化**。
>
> **前置（否则 STOP 1）——锚点仍在**：
> `grep -c 'block-\${blockIdx}' frontend/src/views/ChatView.vue` 为 `1`；
> `grep -c "export function upsertApprovalBlock" frontend/src/composables/useChatStream.ts` 为 `1`；
> `grep -c "export function hasVisibleBlocks" frontend/src/composables/useChatStream.ts` 为 `1`。
>
> **Drift check（先跑）**：`git diff --stat 7865932 -- frontend/src/views/ChatView.vue frontend/src/views/ChatView.test.ts frontend/src/composables/useChatStream.ts frontend/src/composables/useChatStream.test.ts` 必须**无输出**。
>
> **基线（Step 1 之前跑一次，记下失败清单；否则 STOP 2）**：
> `cd frontend && pnpm vitest run src/views/ChatView.test.ts src/composables/useChatStream.test.ts`（超时给足 900 s）。

## Status

- **Priority**: P2（用户正在输入的一行字会跑到另一张卡上，且那张卡的按钮语义完全不同）
- **Effort**: S（一个纯函数 + 两处 key + 2 个测试）
- **Risk**: LOW（只改 `:key` 与新增导出；渲染结果不变）
- **Depends on**: 无
- **Blocks**: 无
- **Planned at**: dev `7865932`，2026-10-26
- **Category**: bug（状态错位 / 数据串卡）

## Why this matters

网页把一轮回答画成"活动线"上的块，块用**下标**做 `:key`：

```vue
<template v-for="(block, blockIdx) in run.items" :key="`${msg.id ?? idx}-block-${blockIdx}`">
```

而块会被**插到列表中间**，最常见的一条是审批记录落到它对应的那张工具卡前面：

```ts
export function upsertApprovalBlock(blocks: TimelineBlock[], next: …): TimelineBlock[] {
  …
  const gated = next.toolStepId
    ? blocks.findIndex((block) => block.kind === 'tool' && block.step.stepId === next.toolStepId)
    : -1
  if (gated >= 0) return [...blocks.slice(0, gated), next, ...blocks.slice(gated)]
  const lastTool = blocks.map((block) => block.kind === 'tool').lastIndexOf(true)
  if (lastTool >= 0) return [...blocks.slice(0, lastTool), next, ...blocks.slice(lastTool)]
  …
}
```

`approval` 与 `tool` 都是"上线"的块（`frontend/src/lib/activityRuns.ts:40` 的 `ON_LINE_KINDS`），
所以这次插入发生在**同一段 run 的中间**：那张工具卡的 `blockIdx` 立刻 +1，它后面每一张卡也都 +1。

Vue 按 key 复用组件实例。key 是下标 ⇒ 第 k 个位置的实例从"卡片 A"变成"卡片 B"，
而**带本地状态的子组件就跟着错位**：

- `frontend/src/components/chat/ToolCallCard.vue:5-8` 用 `:default-open`（`open` prop 在
  `ChatView.vue:194-201` 没有绑定，是可选的），展开状态是实例本地的；
- `frontend/src/components/chat/ToolGateFooter.vue:130` `const words = ref('')`——**用户正在输入的拒绝原话**
  就存在这里。

于是当一条审批记录在用户打字期间落到前一张卡前面时：用户输的那句话出现在**另一张卡**的输入框里，
那张卡上的"发送/拒绝并停下"按钮此刻指向的是另一条命令。这正是 `plans/154` 要保的那份数据
（"拒绝时用户写的那句话要上卡"）——它被保了下来，却被送到了错误的卡上。

修复方式：**key 由块自己提供**。工具块有稳定的 `step.stepId`，其它块在携带 `id` 时用 `id`；
只有两者都没有的块才退回位置（并在函数注释里说明这是最后手段）。

## Current state

`frontend/src/views/ChatView.vue:163-173`（活动线两层 v-for）：

```vue
<template v-for="(run, runIdx) in activityRunsOf(msg)" :key="`${msg.id ?? idx}-run-${runIdx}`">
  …
  <template v-for="(block, blockIdx) in run.items" :key="`${msg.id ?? idx}-block-${blockIdx}`">
```

块类型（`frontend/src/composables/useChatStream.ts:1389-1428`，节选）：

```ts
export type TimelineBlock =
  | { kind: 'prompt'; id?: string; text: string }
  | { kind: 'user'; id?: string; text: string }
  | { kind: 'assistant'; id?: string; text: string; open: boolean }
  | { kind: 'thinking'; id?: string; … }
  | { kind: 'tool'; id?: string; step: SubagentToolStep }      // step.stepId 必有
  | { kind: 'plan'; id?: string; plan: PlanUpdateData }
  | { kind: 'worked'; id: string; … }
  | { kind: 'approval'; id: string; actionId: string; … }       // id 必有
  | { kind: 'error'; id?: string; text: string; detail?: … }
  | { kind: 'compaction'; id: string; … }
  | { kind: 'goal'; id: string; … }
  …
```

`SubagentToolStep.stepId` 是 `string`（`useChatStream.ts:225-227`，必有）。

导出辅助函数的位置惯例：同文件 `:1362-1378` 的 `hasVisibleBlocks`（纯函数、带一段解释性注释、
由 `ChatView` 以 `hasVisibleBlocks(msg.blocks)` 直接调用）。

## Scope

**In scope（只改这些）**：
- `frontend/src/composables/useChatStream.ts`（新增导出 `timelineBlockKey`）
- `frontend/src/views/ChatView.vue`（活动线块 v-for 的 `:key` 换成它）
- `frontend/src/composables/useChatStream.test.ts`（新增 1 个纯函数测试）
- `frontend/src/views/ChatView.test.ts`（新增 1 个挂载测试）

**Out of scope（看着相关也不要动）**：
- `groupActivityRuns` / `blockOnActivityLine` / `activityRuns.ts`（分组规则不变）
- 外层 run 的 `:key`（`-run-${runIdx}`）：`ActivityLine`/`ActivityStep` 本身不带状态，
  换它只会制造无谓的 diff 噪音
- `upsertApprovalBlock` / `placeBeforeGateAnchor` 的插入位置（本计划只让 key 稳定，不改顺序）
- 各卡片组件的内部状态实现（`ToolCallCard` 的展开、`ToolGateFooter` 的 `words`）
- 子代理视图、工作台（它们用 `TimelineBlock` 但不在本计划的渲染路径）

## Commands you will need

| 用途 | 命令 | 成功时 |
|---|---|---|
| 纯函数测试 | `cd frontend && pnpm vitest run src/composables/useChatStream.test.ts -t 'timeline block key'` | PASS |
| 挂载测试 | `cd frontend && pnpm vitest run src/views/ChatView.test.ts -t 'keeps a card'` | PASS |
| 两个文件全测 | `cd frontend && pnpm vitest run src/views/ChatView.test.ts src/composables/useChatStream.test.ts` | 与基线相比无新增 FAIL |
| 类型检查 | `cd frontend && pnpm typecheck` | exit 0 |

## Steps

### Step 1：先写纯函数测试（会红）

在 `frontend/src/composables/useChatStream.test.ts` 的 import 列表里加上 `timelineBlockKey`，并新增：

```ts
describe('timeline block key', () => {
  it('follows the block, not its position', () => {
    const tool = (stepId: string): TimelineBlock => ({
      kind: 'tool',
      step: { stepId, toolName: 'read_file', summary: stepId } as unknown as SubagentToolStep,
    })
    const a = tool('step-a')
    const b = tool('step-b')
    // 同一个块在插入别的东西之后仍然给同一个 key。
    expect(timelineBlockKey(a, 0)).toBe(timelineBlockKey(a, 3))
    expect(timelineBlockKey(a, 0)).not.toBe(timelineBlockKey(b, 0))
    // 带 id 的块同理。
    const approval: TimelineBlock = { kind: 'approval', id: 'act-1', actionId: 'act-1', actionKind: 'shell', status: 'allowed' }
    expect(timelineBlockKey(approval, 0)).toBe(timelineBlockKey(approval, 5))
    // 两者的 id 都缺时只能退回位置（最后手段）。
    expect(timelineBlockKey({ kind: 'assistant', text: 'x', open: false }, 2)).toBe('assistant-at-2')
  })
})
```

（`SubagentToolStep` 的构造按该类型的必填字段写；不确定就先看 `useChatStream.ts:225` 的接口定义，
**不要**用 `any` 糊过去——`pnpm typecheck` 会抓。）

**Verify**：`cd frontend && pnpm vitest run src/composables/useChatStream.test.ts -t 'timeline block key'`
→ FAIL（`timelineBlockKey is not a function`）。若直接 PASS，STOP 3。

### Step 2：实现并接上

在 `useChatStream.ts` 的 `hasVisibleBlocks`（`:1368`）之后新增：

```ts
/**
 * timelineBlockKey is the v-for key of one block on the activity line. It must
 * come from the block itself: a record inserted in the middle (an approval
 * landing before the card it gates, upsertApprovalBlock) shifts every later
 * block's index, and a positional key would then hand a card's component
 * instance — its open state, the refusal draft in its footer — to a different
 * card. A tool block is identified by its step id and every other kind by its
 * own id; only a block that carries neither falls back to the position.
 */
export function timelineBlockKey(block: TimelineBlock, index: number): string {
  if (block.kind === 'tool') return `tool:${block.step.stepId}`
  if ('id' in block && block.id) return `${block.kind}:${block.id}`
  return `${block.kind}-at-${index}`
}
```

`ChatView.vue`：把 `:173` 的 `:key="\`${msg.id ?? idx}-block-${blockIdx}\`"` 换成
`:key="timelineBlockKey(block, blockIdx)"`，并在该文件从 `@/composables/useChatStream` 的 import 列表里
加上 `timelineBlockKey`（那段 import 在 `:751`，已经是按字母/语义混排的长列表，跟着现有风格插入）。

**Verify**：`cd frontend && pnpm vitest run src/composables/useChatStream.test.ts -t 'timeline block key'` → PASS；
`cd frontend && pnpm typecheck` → exit 0。

### Step 3：挂载测试——插入一条审批记录，展开的卡还是那张卡

在 `frontend/src/views/ChatView.test.ts` 里新增（该文件的 `describe('ChatView conversation bubbles')` 之外，
自成一个 describe；`fake`、`mountView` 之类的 helper 按文件里既有用例的写法用）：

```ts
it('keeps a card open when an approval record lands before it', async () => {
  const tool = (stepId: string, header: string): TimelineBlock => ({
    kind: 'tool',
    step: { stepId, toolName: 'read_file', summary: '', header } as unknown as SubagentToolStep,
  })
  const message = {
    id: 'm1', role: 'assistant', content: '',
    blocks: [tool('step-a', 'Read a.txt'), tool('step-b', 'Read b.txt')],
  }
  ;(fake as { messages: { value: unknown[] } }).messages.value = [message]
  // mountView 默认把 ToolCallCard stub 掉（stubs: { ToolCallCard: true }）——必须显式
  // 关掉这个 stub 才有 .tool-card 可找；un-stub 的写法照抄同文件 `mountView({ SubagentTab: false })`。
  const wrapper = await mountView({ ToolCallCard: false })
  const cardOf = (header: string) => wrapper.findAll('.tool-card')
    .find((card) => card.text().includes(header))!

  // 展开 B。
  await cardOf('Read b.txt').find('button').trigger('click')
  expect(cardOf('Read b.txt').attributes('data-state')).toBe('open')

  // 一条审批记录落到 A 前面：A 与 B 的下标都 +1。
  ;(fake as { messages: { value: unknown[] } }).messages.value = [{
    ...message,
    blocks: [
      { kind: 'approval', id: 'act-1', actionId: 'act-1', actionKind: 'shell', status: 'allowed' } as TimelineBlock,
      tool('step-a', 'Read a.txt'),
      tool('step-b', 'Read b.txt'),
    ],
  }]
  await nextTick()
  await nextTick()

  expect(cardOf('Read b.txt').attributes('data-state')).toBe('open')
  expect(cardOf('Read a.txt').attributes('data-state')).toBe('closed')
})
```

**Verify**：`cd frontend && pnpm vitest run src/views/ChatView.test.ts -t 'keeps a card open when an approval record'`
→ 改动前 FAIL（B 变成 closed：实例被位置复用给了 A），改动后 PASS。若这条用例在 Step 2 之前就 PASS，
STOP 4：说明 `Tool`/`Collapsible` 的行为与预期不同，把 `data-state` 的实际值与 DOM 片段贴回来。

### Step 4：两个文件回归

**Verify**：`cd frontend && pnpm vitest run src/views/ChatView.test.ts src/composables/useChatStream.test.ts`
→ 与基线相比无新增 FAIL；`cd frontend && pnpm typecheck` → exit 0。

## Test plan

- `useChatStream.test.ts`：纯函数 `timelineBlockKey` —— 同块不同位置同 key、不同块不同 key、
  带 `id` 的块同理、两者都缺时退回 `kind-at-index`。
- `ChatView.test.ts`：挂载真实组件，展开第二张卡 → 在前面插入一条审批块 → 断言仍是那张卡展开、
  第一张卡仍关闭（这正是 `:default-open` 本地状态错位的可见后果）。
- 组件的挂载方式照抄同文件既有用例（`fake.messages.value = [...]` + `await nextTick()`）。

## Done criteria

机器可检查，全部满足：

- [ ] `cd frontend && pnpm typecheck` exit 0
- [ ] `cd frontend && pnpm vitest run src/views/ChatView.test.ts src/composables/useChatStream.test.ts` 与基线相比无新增 FAIL
- [ ] `grep -c "timelineBlockKey" frontend/src/views/ChatView.vue` 为 `2`（import + 使用）
- [ ] `grep -c 'block-\${blockIdx}' frontend/src/views/ChatView.vue` 为 `0`
- [ ] `grep -c "export function timelineBlockKey" frontend/src/composables/useChatStream.ts` 为 `1`
- [ ] `git status --porcelain` 只显示 Scope 里的四个文件
- [ ] `plans/README.md` 本计划状态行已更新

## STOP conditions

停下报告，不要即兴发挥，如果：

1. 前置锚点的 grep 计数 ≠ 预期，或 drift check 后被引用的行/符号变了。
2. 基线命令本身跑不起来，拿不到失败清单。
3. 纯函数测试在 Step 1 直接 PASS（说明已经有人导出了同名函数——先 `grep -rn timelineBlockKey frontend/src`）。
4. 挂载测试在 Step 2 之前就 PASS（`Collapsible` 也许按内容而非实例保存状态）：贴出 `data-state` 的实际值，
   不要为了让它变红去改断言。
5. `.tool-card` 这个 class 在 `ToolCallCard.vue` 里不再是根元素的 class（本计划基线里它在 `:3`）——
   改用该文件里实际存在的定位方式，并在报告里说明换了什么。
6. 你发现 `run.items` 的 `block` 在某些路径上是**同一个对象被复制**（同一 `stepId` 出现两次）：
   那是另一个缺陷，停下报告，不要用 `index` 塞进 key 里掩盖它。

## Maintenance notes

- 新增一种块时：给它 `id`（能稳定跨插入的），否则它会退化成位置 key，本计划要防的错位在它身上依然存在。
- `tool` 块的 key 用 `step.stepId`；如果将来同一个 `stepId` 会在一条消息里出现两次（例如并行重试的两次调用
  共享 step），key 必须换成 `stepId + 调用序号`，否则 Vue 会报重复 key 并丢实例——那是要单独处理的场景。
- 评审重点：`ChatView.vue` 只改了一处 `:key`；若 diff 里还动了 `groupActivityRuns`、块顺序或卡片组件，
  说明执行者越界了。
