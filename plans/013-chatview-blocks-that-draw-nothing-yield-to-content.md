# Plan 013：ChatView 只在时间线真有可见内容时走块分支——只剩锚点（或只有无确认行的审批块）的回答显示它的文字，不再是空气泡

> **执行者须知**：按步骤顺序执行，每步跑验证命令并确认预期结果后再进下一步。触发
> "STOP conditions" 时停下报告，不要即兴发挥。**不要更新 `plans/README.md`**（评审方维护索引）。
> **禁止提交代码**（owner 手动提交）：不建分支、不 commit、不 push、不 stash；改动留在工作区。
>
> **工作目录**：`/Users/doudou/workspace/unionj-cloud/forebrain-harness-008`（owner 指定的 worktree，
> detached HEAD `9656051`，工作区里已有 plan 008/011/012 的**未提交**改动——那是本计划的基础，不要还原、不要整理）。
> 下文所有相对路径都相对这个目录。
>
> **Drift check（先跑）**：
> ```bash
> cd /Users/doudou/workspace/unionj-cloud/forebrain-harness-008
> grep -n 'export function hasVisibleBlocks' -A3 frontend/src/composables/useChatStream.ts
> grep -n 'msg.blocks?.length' frontend/src/views/ChatView.vue
> git diff --stat 9656051 -- frontend/src/views/ChatView.test.ts      # 预期：无输出
> ```
> 预期：第一条显示 `return (blocks ?? []).some((block) => block.kind !== 'gate')`；第二条恰好两行（`:170` 打字点、`:184` 块分支）；
> 第三条无输出。不符即 STOP。

## Status

- **Priority**: P3
- **Effort**: S
- **Risk**: LOW（只改"有没有可见内容"的判断与一个模板条件）
- **Depends on**: plan 008 的未提交实现（已在工作区）；与 011/012 无代码依赖，但同在一个 worktree，**在 012 之后执行**
- **Category**: bug
- **Planned at**: commit `9656051` + 008/011/012 工作区改动，2026-10-10

## Why this matters

plan 008 给 exit_plan_mode gate 加了一种不画任何东西的"锚点块"（`kind: 'gate'`），并新增 `hasVisibleBlocks` 让
`messageHasBubble` 不再把只含锚点的回答当成有内容。但 `ChatView.vue` 的模板仍用 `msg.blocks?.length` 决定走**块分支**：

```
<template v-else-if="msg.blocks?.length">  ← 只要有任何块（包括锚点）就走这里
  …逐块渲染（锚点什么都不画）…
</template>
…
<MessageResponse v-else-if="String(msg.content ?? '').trim()" … />   ← 被上面截走，永远到不了
```

可复现场景（探针实证）：模型不说话直接调 exit_plan_mode，用户在 web 上停止。`withCancelledTurn` 把回答的 `content` 设成
"Request cancelled"（中文"请求已取消"），`blocks` 只有锚点 → `messageHasBubble` 为真（有 content）→ 气泡画出来 → 模板走块分支
→ 什么都不画：**一个空气泡，取消提示被藏掉**。`WorkshopView.vue` 已经用 `hasVisibleBlocks` 做这个判断，所以 Workshop 页正常，两页不一致。

同一类问题：`ApprovalCard.vue` 在没有确认行（`confirmation`）时什么都不画（`v-if="line"`），但 `hasVisibleBlocks` 把这种审批块算作"可见"，
只含这种块的回答同样会是空气泡。

## Current state

`frontend/src/composables/useChatStream.ts:910-916`（008 新增）：

```ts
/**
 * hasVisibleBlocks reports whether a timeline draws anything: a gate anchor
 * only marks where a card-less call sits.
 */
export function hasVisibleBlocks(blocks?: TimelineBlock[]): boolean {
  return (blocks ?? []).some((block) => block.kind !== 'gate')
}
```

`frontend/src/components/chat/ApprovalCard.vue`（只读，不改）：

```vue
  <div
    v-if="line"
    ...
const line = computed(() => String(props.block.confirmation ?? '').trim())
```

`frontend/src/views/ChatView.vue:170`（打字点，**不改**）与 `:184`（块分支，**改**）：

```vue
                      <div v-if="msg.role === 'assistant' && isStreaming && !msg.blocks?.length && !String(msg.content ?? '').trim()"
...
                      <template v-else-if="msg.blocks?.length">
```

`ChatView.vue:651` 已 import `hasVisibleBlocks`（008 加的），`messageHasBubble`（`:1496` 起）已在用它。

`frontend/src/views/WorkshopView.vue:41`（参照，不改）：`v-if="message.role === 'assistant' && hasVisibleBlocks(message.blocks)"`，
其后 `v-else-if="message.role !== 'assistant' || String(message.content ?? '').trim()"` 渲染文字。

008 的单测（`frontend/src/composables/useChatStream.test.ts:2167-2171` 附近，用例
`'places a gate record after the records already at its anchor, and only a gate draws nothing'`）：

```ts
    expect(hasVisibleBlocks([anchor])).toBe(false)
    expect(hasVisibleBlocks(undefined)).toBe(false)
    expect(hasVisibleBlocks([anchor, assistant])).toBe(true)
```

`frontend/src/views/ChatView.test.ts`：用 `vi.mock('@/composables/useChatStream', …)` 替换 composable，`fake` 对象暴露
`sessionId / subagents / …`，但 `messages: ref([])` 是在工厂里新建的、测试拿不到；`mountView()`（`:126` 起）挂载 ChatView，
stubs 里有 `ApprovalCard: true`、`ToolCallCard: true` 等，**没有** stub `MessageResponse`（真组件渲染文字）。

约定：注释写"为什么"、英文完整句子；测试名用描述行为的英文句子（看该文件既有用例）。

## Commands you will need

| 用途 | 命令（在 `frontend/` 下） | 成功预期 |
|---|---|---|
| 单文件 | `npx vitest run src/composables/useChatStream.test.ts` | 全部通过 |
| 单文件 | `npx vitest run src/views/ChatView.test.ts` | 全部通过 |
| 全量 | `pnpm test` | 全部通过 |
| 类型检查 | `npx vue-tsc --noEmit -p tsconfig.json` | exit 0、无输出 |
| 构建 | `pnpm build` | 成功（重写 `../pkg/gateway/dist`，预期产物） |

## Scope

**In scope**：
- `frontend/src/composables/useChatStream.ts`——只改 `hasVisibleBlocks` 的实现与 doc 注释
- `frontend/src/composables/useChatStream.test.ts`——扩充上面那条用例的断言
- `frontend/src/views/ChatView.vue`——只改 `:184` 的条件
- `frontend/src/views/ChatView.test.ts`——mock 暴露 `messages`，新增一个 describe
- `pkg/gateway/dist`（`pnpm build` 生成）

**Out of scope**：
- `ChatView.vue:170` 的打字点条件（008 已裁定：锚点存在时不显示三点，可接受）。
- `ApprovalCard.vue`、`WorkshopView.vue`、`SubagentConversation.vue`。
- 时间线块模型、事件处理（`useChatStream.ts` 除 `hasVisibleBlocks` 以外的任何代码）。

## Git workflow

不建分支、不 commit、不 push、不 stash（owner 手动提交）。改动留在 worktree 工作区。

## Steps

### Step 1：`hasVisibleBlocks` 只认真会画出东西的块

把实现与注释改为：

```ts
/**
 * hasVisibleBlocks reports whether a timeline draws anything. A gate anchor
 * only marks where a card-less call sits, and an approval record draws only
 * the line the surface printed for it (ApprovalCard) - one with no line draws
 * nothing. A turn whose blocks draw nothing shows its text instead.
 */
export function hasVisibleBlocks(blocks?: TimelineBlock[]): boolean {
  return (blocks ?? []).some((block) => {
    if (block.kind === 'gate') return false
    if (block.kind === 'approval') return Boolean(String(block.confirmation ?? '').trim())
    return true
  })
}
```

在 008 那条用例里（`hasVisibleBlocks([anchor, assistant])` 之后）补断言：

```ts
    const pending: TimelineBlock = { kind: 'approval', id: 'approval-1', actionId: 'act-1', actionKind: 'shell', status: 'pending' }
    const printed: TimelineBlock = { ...pending, status: 'approved', confirmation: '✔ You approved forebrain to run git status' }
    expect(hasVisibleBlocks([pending])).toBe(false)
    expect(hasVisibleBlocks([anchor, pending])).toBe(false)
    expect(hasVisibleBlocks([printed])).toBe(true)
```

（若 `approval` 成员的必填字段与此不同，按 `TimelineBlock` 的实际定义补齐，以 vue-tsc 通过为准。）

**Verify**：`npx vitest run src/composables/useChatStream.test.ts -t 'only a gate draws nothing'` → passed；`npx vue-tsc --noEmit -p tsconfig.json` → 无输出。

### Step 2：ChatView 的块分支用同一个判断

`ChatView.vue:184`：`<template v-else-if="msg.blocks?.length">` → `<template v-else-if="hasVisibleBlocks(msg.blocks)">`。
不改 `:170`、不改其他行。

**Verify**：`git diff frontend/src/views/ChatView.vue | grep '^[-+][^-+]'` 只比 008 时多出这一处 `-`/`+`（共两行变化：一行 `-`、一行 `+`，内容如上）；
`npx vue-tsc --noEmit -p tsconfig.json` → 无输出。

### Step 3：ChatView 组件测试

`frontend/src/views/ChatView.test.ts`：
1. 在 mock 工厂里照 `f.subagents = ref([])` 的写法加 `f.messages = ref([])`（并在 `f` 的类型声明里加 `messages: unknown`），
   把 `useChatStream: () => ({ … messages: ref([]), … })` 改为 `messages: f.messages`。
2. 新增 `describe('ChatView conversation bubbles', …)`：beforeAll 与 `mountView()` 复用既有的（若 `mountView` 定义在另一个 describe 内部，
   把它和 `makeRouter` 一样提到文件顶层，或在新 describe 里照抄一份——两种都可，选改动最小的）；afterEach 把 `fake.messages.value = []`。
   用例 `it('shows a turn’s words when its blocks draw nothing', …)`：

```ts
;(fake as { messages: { value: unknown[] } }).messages.value = [
  { id: 'u1', role: 'user', content: 'submit the plan' },
  { id: 'a1', role: 'assistant', content: 'Request cancelled', blocks: [{ kind: 'gate', id: 'gate:call-a', stepId: 'call-a' }] },
  { id: 'u2', role: 'user', content: 'try again' },
  { id: 'a2', role: 'assistant', content: 'here is the plan', blocks: [
    { kind: 'assistant', text: 'here is the plan', open: false },
    { kind: 'gate', id: 'gate:call-b', stepId: 'call-b' },
  ] },
]
const wrapper = await mountView()
await nextTick()
const text = wrapper.text()
expect(text).toContain('Request cancelled')                        // 只有锚点的回答显示它的文字
expect(text.split('here is the plan').length - 1).toBe(1)          // 有可见块的回答只画块，不重复画 content
wrapper.unmount()
```

**Verify**：`npx vitest run src/views/ChatView.test.ts` → 全部通过（含新用例）；**反证**：临时把 Step 2 的改动还原成
`msg.blocks?.length` 再跑一次 → 新用例失败于 `toContain('Request cancelled')`；然后恢复 Step 2。

### Step 4：全量与构建

```bash
cd /Users/doudou/workspace/unionj-cloud/forebrain-harness-008/frontend
pnpm test
npx vue-tsc --noEmit -p tsconfig.json
pnpm build
```

**Verify**：测试全绿；vue-tsc 无输出；构建成功。

## Test plan

- `useChatStream.test.ts`：`hasVisibleBlocks` 对"无确认行审批块"返回 false、对"有确认行审批块"返回 true（扩充 008 用例）。
- `ChatView.test.ts`：只含锚点的回答显示文字；有可见块的回答不重复显示 content（新增，带反证）。

## Done criteria

- [ ] `cd frontend && pnpm test` 全绿；`npx vue-tsc --noEmit -p tsconfig.json` 无输出；`pnpm build` 成功
- [ ] `grep -n 'v-else-if="hasVisibleBlocks(msg.blocks)"' frontend/src/views/ChatView.vue` 有 1 行；`grep -c 'msg.blocks?.length' frontend/src/views/ChatView.vue` = 1（只剩打字点那处）
- [ ] Step 3 的反证做过且结果为"还原即失败"，写进报告
- [ ] `git status --short` 中本计划新增的改动只涉及 Scope 列出的文件

## STOP conditions

1. Drift check 不符。
2. 改了 `hasVisibleBlocks` 后任何既有用例变红（说明有别处依赖"无确认行的审批块算可见"）——停下报告是哪条。
3. `mountView` 挂载时 `MessageResponse` 不渲染文字（`wrapper.text()` 里找不到 `here is the plan`）——停下报告，不要改断言去迁就。
4. 需要改 Out of scope 的文件。

## Maintenance notes

- 判断"一轮回答画不画东西"只用 `hasVisibleBlocks`，不要用 `blocks.length`。新增一种可能什么都不画的块（或让某种块在某些状态下不画）时，同步改 `hasVisibleBlocks`。
- `ChatView.vue:170` 的打字点仍按 `blocks.length` 判断（008 的裁定）；若将来要求"gate 停靠时也显示三点"，改那一处并补用例。
- `ChatView.test.ts` 在主树 `005c1a8` 有 owner 的 1 行改动，并回时注意这个文件的合并。
