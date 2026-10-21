# Plan 091: 文件树只有一个展开状态的所有者；上下文环真的落在「上下文」标签

> **Executor instructions**: 按步骤执行，每步跑完「Verify」并确认输出符合预期再进入下一步。
> 命中「STOP conditions」任一条就停下来报告，不要即兴改动。全部完成后更新
> `plans/README.md` 里本计划那一行的状态（除非派发你的评审方说他维护索引）。
>
> **Drift check（先跑）**: `git diff --stat 8d3322c..HEAD -- ai-elements-vue/packages/elements/src/file-tree/FileTree.vue frontend/src/composables/useWorkspaceTree.ts frontend/src/components/chat/WorkspaceTree.vue frontend/src/components/chat/WorkspaceRow.vue frontend/src/components/chat/ChatWorkbench.vue frontend/src/composables/useWorkbench.ts`
> 若有变动，先把「Current state」的摘录与现码对照；对不上按 STOP 处理。

## Status

- **Priority**: P2
- **Effort**: M
- **Risk**: MED
- **Depends on**: none
- **Category**: bug
- **Planned at**: commit `8d3322c`, 2026-10-21

## Why this matters

两处"看似能用、其实失效"的交互都在工作台里。**文件树**的展开状态有两份：`useWorkspaceTree`（应用侧，`Set` 原地改）和
`FileTree.vue`（库侧，`togglePath` 换成一个副本）。库侧只在 `props.expanded` **引用变化**时同步，而应用侧从不换引用——
于是用鼠标点过一次目录之后，库侧的副本就与应用侧永久分叉：此后按 Enter / ← / → 只会改应用侧（子项被 `v-if` 建起来又拆掉），
而库侧的 `Collapsible` 停在旧状态，内容被关在关着的面板里，表现为"按了没反应"。**上下文环**是另一处：
`workbench.openOn('context')` 写了 `requested`，但 `ChatWorkbench` 挂载时先应用它、紧接着又被"上次读过的标签"覆盖，
且 `consumed()` 全仓零调用——点环打开的永远是上次那一页，正是这次改动声称要修掉的旧行为。
修完之后：树的展开状态只有一个所有者（鼠标与键盘同源）；点上下文环落在上下文，且手动开面板仍回到上次读过的标签。

## Current state

- `ai-elements-vue/packages/elements/src/file-tree/FileTree.vue:23-34` — 库侧的副本与"只认同引用"的同步：

```ts
const internalExpanded = ref(new Set(props.defaultExpanded))
...
watch(
  () => props.expanded,
  (newVal) => { if (newVal) { internalExpanded.value = newVal } },
  { immediate: true },
)
```

  `:46-58` 的 `togglePath` 把集合换成一个**新副本**并 `emit('expandedChange', newExpanded)`：

```ts
function togglePath(path: string) {
  const newExpanded = new Set(internalExpanded.value)
  if (newExpanded.has(path)) newExpanded.delete(path); else newExpanded.add(path)
  internalExpanded.value = newExpanded
  emit('expandedChange', newExpanded)
}
```

- `ai-elements-vue/packages/elements/src/file-tree/FileTreeFolder.vue:31` + `:42` + `:82` —
  折叠面板的开关与内容都吃库侧那份：`const isExpanded = computed(() => expandedPaths.value.has(props.path))`、
  `<Collapsible :open="isExpanded">`、`<CollapsibleContent><slot /></CollapsibleContent>`。
- `frontend/src/composables/useWorkspaceTree.ts:26`（`const trees = reactive(new Map<string, WorkspaceTreeState>())`）
  与 `:83-94` 的 `toggle` —— 应用侧**原地** `add/delete`，引用永不变化：

```ts
function toggle(path: string): void {
  if (!key.value) return
  ensureTree(key.value)
  const open = expanded.value
  if (open.has(path)) { open.delete(path); return }
  open.add(path)
  ...
}
```

- `frontend/src/components/chat/WorkspaceTree.vue:88-102` 的 `onExpandedChange(next)` 同样原地改应用侧的 `Set`。
- `frontend/src/components/chat/WorkspaceRow.vue:27` 的 `<template v-if="isExpanded">`（应用侧）与 `:150-163` 的键盘绑定
  （Enter / ← / → 调 `toggle`，即应用侧的原地改）。

  结论（可静态确定）：鼠标点过一次之后，库侧的 `internalExpanded` 是独立副本；此后键盘只改应用侧 ⇒
  `aria-expanded` 变了、子树被建/拆，但 `Collapsible` 不动 ⇒ 内容看不见。
- `frontend/src/composables/useWorkbench.ts:14-34` — 模块级 `requested`，`consumed()` **零调用**：

```ts
const requested = ref<string | null>(null)
...
openOn(tab: string) { requested.value = tab; open.value = true },
consumed() { requested.value = null },
```

- `frontend/src/components/chat/ChatWorkbench.vue:306-316` — 两个 `immediate` watcher，后一个覆盖前一个：

```ts
watch(() => props.requestedTab, (tab) => { if (!tab) return; select(tab); revealActive() }, { immediate: true })
watch(scopeKey, () => { active.value = readStored() }, { immediate: true })
```

  `readStored()`（`:180-185`）读 `localStorage` 的上一标签；`ChatView.vue:529-537` 用 `v-if` 挂载面板
  （每次打开都是新挂载，两个 immediate 都会跑）。
- 设计约束（必须遵守）：`docs/plan/WEB_REDESIGN_AND_GATEWAY_ACCESS/app-preview.html` 是 web 的唯一验收基准；
  上下文环"点它打开上下文"是原型 `:4346` 的行为（`ChatWorkbench.vue:303-306` 的注释也写着 prototype `:4346`）。
  本次改动**不改稿面**，只让实现符合它。
- e2e 现状：`frontend/e2e/workspace-tree.spec.ts:147-159` 的键盘用例只断言 `aria-expanded`（应用侧决定），
  从不先鼠标点一次、也从不检查子项是否真的出现——所以它抓不到这个缺陷。

## Commands you will need

| Purpose | Command | Expected on success |
|---|---|---|
| 库单测 | `cd ai-elements-vue/packages/elements && pnpm test` | 全 passed |
| 前端单测 | `cd frontend && pnpm test -- src/components/chat/WorkspaceTree.test.ts src/components/chat/ChatWorkbench.test.ts` | 全 passed |
| 前端全量 | `cd frontend && pnpm test` | 全 passed |
| 类型检查 | `cd frontend && pnpm exec vue-tsc --noEmit` | exit 0 |
| e2e（树） | `FOREBRAIN_E2E_SPEC=e2e/workspace-tree.spec.ts scripts/acceptance/web_e2e.sh` | `web e2e: PASS` |
| e2e（工作台其他面） | `FOREBRAIN_E2E_SPEC="e2e/workbench-history.spec.ts e2e/message-cards.spec.ts" scripts/acceptance/web_e2e.sh` | `web e2e: PASS` |

## Scope

**In scope**：
- `ai-elements-vue/packages/elements/src/file-tree/FileTree.vue`
- `frontend/src/components/chat/ChatWorkbench.vue` + `frontend/src/components/chat/ChatWorkbench.test.ts`
- `frontend/src/composables/useWorkbench.ts`（只在必要时加注释/语义，接口保持）
- `frontend/src/views/ChatView.vue`（只加 `@requested-tab-shown` 接线）
- `frontend/e2e/workspace-tree.spec.ts`（加强键盘用例）

**Out of scope**：
- `frontend/src/composables/useWorkspaceTree.ts` 的原地改：它是"鼠标与键盘的唯一真相"，本计划**不**改成换引用；
  要改的是库侧（见 Step 1）。除非 Step 2 的验证证明原地改无法被库侧感知，才回来讨论（那时先 STOP 报告）。
- `ai-elements-vue/packages/elements/src/file-tree/FileTreeFolder.vue`：`provide(FileTreeFolderKey, …)` 无人消费，
  由 093 的清理负责，本计划不动。
- 工作台标签的样式/宽度/滚动（079 已定稿）、抽屉与工作台的互斥（078）：不要碰。
- 设计稿 `app-preview.html`：不改。

## Git workflow

- 直接改当前 `dev` 工作区；**不 commit、不 push**，除非派发方明确要求。
- 若要求提交：`fix(web): …`，body 用 `Why:` / `What:` / `Verification:`，`git commit -s`。

## Steps

### Step 1: 库侧把 `expanded` 当**唯一**真相（受控模式不再留副本）

`ai-elements-vue/packages/elements/src/file-tree/FileTree.vue`：

```ts
// Controlled when `expanded` is given: the caller's Set is the state, and this
// component only reports the toggle. Uncontrolled otherwise (defaultExpanded /
// no set at all). Keeping a copy in controlled mode is what let the caret and
// the caller's own expand state drift apart.
const uncontrolled = ref(new Set(props.defaultExpanded))
const expandedSet = computed<Set<string>>(() => props.expanded ?? uncontrolled.value)

function togglePath(path: string) {
  const next = new Set(expandedSet.value)
  if (next.has(path)) next.delete(path); else next.add(path)
  if (!props.expanded) uncontrolled.value = next
  emit('expandedChange', next)
}
```

- 删掉原来那个 `watch(() => props.expanded, …)`。
- `provide(FileTreeKey, { expandedPaths: expandedSet, togglePath, selectedPath: internalSelectedPath, onSelect })`
  ——`expandedPaths` 现在是 `ComputedRef<Set<string>>`；把 `context.ts` 的类型从 `Ref`/`Readonly<Ref>` 放宽到可接受 `ComputedRef`
  （若已经是 `Ref` 则无需改），并确认 `FileTreeFolder.vue:31` 的 `expandedPaths.value.has(...)` 照旧成立。
- `selectedPath` 的同步逻辑保持原样（本计划不动选中态）。

**Verify**: `cd ai-elements-vue/packages/elements && pnpm test` → 全 passed；
`grep -n "internalExpanded" ai-elements-vue/packages/elements/src/file-tree/FileTree.vue` → 无输出。

### Step 2: 应用侧确认无需改动即可被库侧感知

`useWorkspaceTree.expanded` 来自 `reactive(new Map())` 里的 `Set`，是响应式集合代理，原地 `add/delete` 会被追踪。
执行时用一个最小用例证明它（Step 5 的 WorkspaceTree 用例），**不要**为了"保险"把 `toggle` 改成换引用——那会连带
`refresh()`/`visiblePaths()` 的语义一起动。

**Verify**: 见 Step 5 的用例（先写用例，红→绿）。

### Step 3: 上下文环落到它自己的标签，且请求会被消费

`frontend/src/components/chat/ChatWorkbench.vue`：把 `:306-316` 的两个 watcher 合成一个，顺序固定为
"先按 scope 初始化，再让显式请求覆盖"：

```ts
// One initialiser, so the order cannot flip: the tab the conversation last read
// is the default, and a control that is *about* one tab overrides it.
watch([scopeKey, () => props.requestedTab], ([, tab]) => {
  active.value = tab && tabEnabled(tab) ? tab : readStored()
  if (tab) { revealActive(); emit('requested-tab-shown') }
}, { immediate: true })
```

- `requestedTab` 变化（同一个面板里再点一次环）也要生效：上面的 watcher 覆盖了这种情况，但**第二次点同一个环不会改变
  prop 值**——因此 `ChatView` 必须在消费后把它清回 `null`（Step 4），下一次 `openOn` 才会产生"新值"。
- 声明新事件 `(e: 'requested-tab-shown'): void`。
- 删掉原来的两个单 watcher。

**Verify**: `cd frontend && pnpm exec vue-tsc --noEmit` → exit 0。

### Step 4: `ChatView` 与 `useWorkbench` 收口

`frontend/src/views/ChatView.vue:529-537` 的 `<ChatWorkbench>` 上：

```html
:requested-tab="workbench.requested.value"
@requested-tab-shown="workbench.consumed()"
```

`frontend/src/composables/useWorkbench.ts` 的 `consumed()` 保持不变（这是它的第一个真实调用点），
并把注释改成与实现一致（"面板显示过之后清掉，下一次点同一个环才算新的请求"）。
`WorkshopView.vue:89` 也调 `openOn('context')`，检查它挂的 `<ChatWorkbench>`（若有）同样接上该事件；
若该视图没有面板实例，在报告里说明一句即可。

**Verify**: `grep -rn "consumed()" frontend/src` → 至少命中 `useWorkbench.ts` 与 `ChatView.vue` 各一处。

### Step 5: 用例

1. `frontend/src/components/chat/WorkspaceTree.test.ts`（沿用现有 mount 方式，把 `@repo/elements` 的 FileTree 换真件）：
   - **鼠标→键盘**：点击目录 ⇒ 子项可见；再按 `ArrowRight`/`ArrowLeft`（在目录行上）⇒ 子项可折叠/展开，
     且 `aria-expanded` 与"子项是否可见"一致；
   - 纯键盘（不先用鼠标）：`ArrowRight` ⇒ 子项可见（钉住"原本能用"的路径不被改坏）。
2. `frontend/src/components/chat/ChatWorkbench.test.ts`：
   - `localStorage` 里记着 `tree`、`requestedTab='context'` ⇒ 挂载后 `active === 'context'`；
   - `requestedTab=null` ⇒ `active === 'tree'`（上次读过的）；
   - 传入 `requestedTab='context'` 时 `emit('requested-tab-shown')` 恰好一次；
   - `requestedTab` 指向被禁用的标签 ⇒ 退回 `overview`/`readStored()`，不报错。
3. `frontend/e2e/workspace-tree.spec.ts:147-159` 的键盘用例改成**先鼠标开合一次**，再按方向键，
   并断言 `rowByName(page, 'readme.md')` **可见**（不再只断言 `aria-expanded`）。

**Verify**: `cd frontend && pnpm test -- src/components/chat/WorkspaceTree.test.ts src/components/chat/ChatWorkbench.test.ts` → 全 passed。

### Step 6: 全量回归与真机抽查

**Verify**:
1. `cd ai-elements-vue/packages/elements && pnpm test` → 全 passed。
2. `cd frontend && pnpm test` → 全 passed；`pnpm exec vue-tsc --noEmit` → exit 0。
3. `FOREBRAIN_E2E_SPEC=e2e/workspace-tree.spec.ts scripts/acceptance/web_e2e.sh` → `web e2e: PASS`。
4. 真机（fake provider）：鼠标点开 `docs` → 关掉 → 按 `→` ⇒ 子项立刻出现；点输入框右侧的上下文环 ⇒ 面板打开在
   「上下文」标签；关掉面板、用顶栏按钮重开 ⇒ 回到上次读过的标签（不是上下文）。

反向验证（必须做）：
- 把 Step 1 的 `expandedSet` 改回"受控也留副本"（即 `const expandedSet = ref(new Set(props.expanded ?? []))`），
  跑 Step 5.1 的"鼠标→键盘"用例 ⇒ 必须变红；改回后变绿。
- 把 Step 3 的两个 watcher 恢复成原来的顺序 ⇒ Step 5.2 的第一条必须变红。

## Test plan

见 Step 5。参照：`frontend/src/components/chat/WorkspaceTree.test.ts` 现有 mount 与 `useWorkspaceTree` 的注入方式；
`ChatWorkbench.test.ts` 现有的 `localStorage` 用法（`grep -n "localStorage" frontend/src/components/chat/ChatWorkbench.test.ts`）。

## Done criteria

- [ ] `cd ai-elements-vue/packages/elements && pnpm test` exit 0
- [ ] `cd frontend && pnpm test` exit 0；`pnpm exec vue-tsc --noEmit` exit 0
- [ ] `grep -n "internalExpanded" ai-elements-vue/packages/elements/src/file-tree/FileTree.vue` → 无输出
- [ ] `grep -rn "requested-tab-shown" frontend/src` → `ChatWorkbench.vue` 与 `ChatView.vue` 各命中
- [ ] `FOREBRAIN_E2E_SPEC=e2e/workspace-tree.spec.ts scripts/acceptance/web_e2e.sh` → PASS
- [ ] `git status --porcelain` 只含 Scope 列出的文件
- [ ] `plans/README.md` 本计划状态行已更新

## STOP conditions

- `context.ts` 里 `expandedPaths` 的类型要求可写 `Ref`（例如某处对它赋值）——那么 Step 1 的 `ComputedRef` 会破类型，
  停下来报告并给出"把写入点收敛到 `togglePath`"的改法，不要改成 `shallowRef` 之类。
- `useWorkspaceTree` 的 `expanded` 不是响应式集合（例如被 `toRaw` 或 `shallowReactive` 持有）⇒ Step 2 的假设不成立，
  停下来报告。
- `ChatWorkbench` 的 `active` 初始化还有第三处写入（搜索 `active.value =`），说明面板生命周期比本计划假设的复杂。
- e2e 的键盘用例只能在放宽断言后通过（例如改回只断言 `aria-expanded`）。
- 任一 Verify 连试两次仍失败。

## Maintenance notes

- **受控组件的规矩**：`FileTree` 给了 `expanded` 就是受控——状态属于调用方，组件只报告。以后再加受控 prop
  （`selectedPath` 已经这样，但它是"抄一份"的老写法）时，别再留副本，否则同一类"点过鼠标键盘就死"还会出现。
- `useWorkspaceTree` 的 `toggle` 必须继续"原地改"（`refresh()`/`visiblePaths()`/`ensureRoot` 都依赖同一个集合对象）；
  要换引用就得同时改这三处调用。
- `requested` 是模块级单例：**消费即清空**是它唯一的正确用法；新增"点某个控件打开某个标签"时，接
  `@requested-tab-shown="workbench.consumed()"`，否则下一个用户会被上一次的请求劫持。
- 评审要点：受控/非受控两条路径都验证过（`defaultExpanded` 仍可用）；键盘用例真的断言了子项可见；
  环的第二次点击也生效（因为消费后 prop 变回 `null`）。
