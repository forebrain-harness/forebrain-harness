# Plan 289: 网页交接选择器按 Esc 真正关闭，且不再偷换成评审列表

> **Executor instructions**：按步骤执行，每一步都跑完验证命令并确认预期输出再进入下一步。
> 遇到「STOP 条件」里任意一条就停下报告，不要自行发挥。完成后更新 `plans/README.md`
> 里本计划的状态行（除非派发你的评审者说他维护索引）。
>
> **Drift check（开工第一步）**：
> `cd /Users/doudou/workspace/unionj-cloud/forebrain-harness && git status --porcelain -- frontend/src/components/chat/PlanApprovalDock.vue frontend/src/components/chat/PlanApprovalDock.test.ts frontend/src/composables/usePendingActions.ts && git diff d715dafd -- frontend/src/components/chat/PlanApprovalDock.vue frontend/src/components/chat/PlanApprovalDock.test.ts frontend/src/composables/usePendingActions.ts`
> 若三个文件在 `d715dafd` 之后已有改动，把「当前状态」的摘录与活代码比对；对不上即 STOP。

## Status

- **Priority**: P1
- **Effort**: S
- **Risk**: LOW
- **Depends on**: none
- **Category**: bug
- **Planned at**: commit `d715dafd`, 2026-11-11

## Why this matters

网页版退出计划模式新增了「交给其他模型执行」按钮，它和「请其他模型评审」共用同一个模型选择器，靠 `modelPickPurpose`（`'review' | 'handoff'`）区分后续动作。当前 Esc 处理函数在「选择器打开」时调用的是带默认参数 `'review'` 的 `toggleModelPicker`，而 `toggleModelPicker` 的 `open` 计算会把「当前 purpose 与传入 purpose 不同」也算作「要打开」。结果：在交接选择器上按 Esc，列表不消失，反而静默变成评审列表；用户以为取消成功，随手点一行会触发一次**计划评审**而不是交接，`actionsApprove` 永远不会被调用。这是一个明确的用户可见错误动作，且现有单测完全没覆盖 Esc 与交接选择器的组合。

## Current state

仓库：`/Users/doudou/workspace/unionj-cloud/forebrain-harness`（Go 1.26 后端 + Vue 3 前端，`frontend/` 用 pnpm 10 / vitest）。

- `frontend/src/components/chat/PlanApprovalDock.vue` — 退出计划模式的审批面板；根节点绑定 Esc。
  - `:13`：`<div class="xp-dock" ... @keydown.esc.stop.prevent="onEsc">`
  - `:272-275`：
    ```ts
    const picking = computed(() => Boolean(store.modelPicking.value[props.action.id]))
    /** What the open picker is for: a reviewer, or the model the plan is handed to
     *  (plan 283). One list either way, two captions and two verdicts. */
    const pickPurpose = computed(() => store.modelPickPurpose.value[props.action.id] ?? 'review')
    ```
  - `:52-63`：选择器展开时按 `pickPurpose` 选标题与 `data-testid`（`'exit-plan-hand-off-model'` / `'plan-review-model'`）。
  - `:259-263`（解构 store）：
    ```ts
    const {
      exitPlanRequest, approveExitPlan, keepPlanning, keepPlanOnly, openPlanFeedback, closePlanFeedback,
      toggleModelPicker, requestReview, stopReview, modelLabel, activeReviewLabel, clearContextLabel,
      handOffTo, reviewAgain, refuseAndStop,
    } = store
    ```
  - `:351-371`（缺陷所在）：
    ```ts
    function handOffChosen() {
      closePlanFeedback(props.action)
      toggleModelPicker(props.action, 'handoff')
    }

    function pickModel(model: PlanReviewModelOption) {
      if (pickPurpose.value === 'handoff') {
        void handOffTo(props.action, model)
        return
      }
      void requestReview(props.action, model)
    }

    /** Esc backs the model picker out; on the words it is the wordless refusal —
     *  the plan approval's stop, not the field's collapse (129). */
    async function onEsc() {
      if (picking.value) {
        toggleModelPicker(props.action)
        return
      }
      if (feedbackOpen.value) await refuseAndStop(props.action)
    }
    ```
    注意注释明确承诺 "Esc backs the model picker out"，实现与之相悖。

- `frontend/src/composables/usePendingActions.ts` — 审批状态与动作。
  - `:409-413`：
    ```ts
    function toggleModelPicker(a: ActionRow, purpose: 'review' | 'handoff' = 'review') {
      const open = !modelPicking.value[a.id] || modelPickPurpose.value[a.id] !== purpose
      modelPicking.value = { ...modelPicking.value, [a.id]: open }
      modelPickPurpose.value = { ...modelPickPurpose.value, [a.id]: purpose }
    }
    ```
    交接态下 `modelPicking[a.id] === true`、`modelPickPurpose[a.id] === 'handoff'`，用默认 `'review'` 调用得到 `open = false || true = true`，并把 purpose 改成 `'review'`。
  - `:560-...`：`return { ..., toggleModelPicker, requestReview, handOffTo, reviewAgain, ... }` —— 新增函数必须在这里导出。
  - `:436-441`：`handOffTo` 发送 `handOffModel: { id: String(model.id ?? ''), provider, model }` 给 `forebrainApi.actionsApprove`；注释写 "refuses an empty one"。

- `frontend/src/components/chat/PlanApprovalDock.test.ts` — 组件单测。
  - `:36-64` `mountDock(...)` 样板；`beforeAll(() => setLocale('zh'))`。
  - `:199-221` `'hands the plan to the row the user picks'` 是交接选择器的现成用例（用 `handOffAvailable: true` + 两行 model，`trigger('click')`，断言 `actionsApprove` 参数）。**照它写新用例**。
  - 全文没有 Esc + 交接选择器的组合用例（`grep -nE 'Escape|onEsc' frontend/src/components/chat/PlanApprovalDock.test.ts` 在 `:121` 只有反馈输入框的 Esc）。

仓库约定：
- 文案与按钮标签是 owner 逐字定稿的，**不要改任何中文标题**（`t('chat.exitPlanHandOffChoose')` 等）。
- 「一个引擎、两个界面」：本次只修网页呈现层，不改 `pkg/turn` / `pkg/gateway` 的语义。
- 测试写在既有 `*.test.ts` 里；不新建测试文件，避免 `pkg/architecture` 的命名规则与前端目录约定被打破。
- `plans/` 与 `docs/plan/` 被 `.gitignore` 忽略，`git status` 看不到它们，不能用「工作区干净」推断计划文件没动过。

## Commands you will need

| Purpose | Command | Expected on success |
|---------|---------|---------------------|
| 定向单测 | `cd frontend && pnpm exec vitest run src/components/chat/PlanApprovalDock.test.ts` | `1 passed (1)` 文件、`10+N tests` 全 PASS，exit 0 |
| 前端类型检查 | `cd frontend && pnpm typecheck` | exit 0，0 errors |
| 前端构建 | `cd frontend && pnpm build` | exit 0 |
| 全量前端单测（**非门禁**） | `cd frontend && pnpm test` | 1382 个用例全 PASS，但 **exit 1**：既有 10 个 unhandled rejection（`frontend/src/components/models/ModelFormDrawer.vue:575` 的 `capsTimer` TDZ，本计划范围外）。把输出记进报告，不要修、也不要拿它当门禁 |
| 漂移检查 | 见文件顶部 Drift check | 三条路径无意外改动 |

> `pnpm test -- <路径>` **不会**过滤：脚本是 `vitest run`，加了 `--` 后路径参数被丢弃，跑的是全量并因上面那 10 个既有错误以 exit 1 结束（2026-11-11 实测 181 files / 1382 tests / 10 errors）。定向单测必须用 `pnpm exec vitest run <路径>`。

（`frontend/node_modules` 已存在；若 `pnpm` 提示缺依赖，先 `cd frontend && CI=true corepack pnpm install --frozen-lockfile`。）

## Scope

**In scope**（只允许改这三个文件）：
- `frontend/src/composables/usePendingActions.ts` —— 新增并导出 `closeModelPicker`。
- `frontend/src/components/chat/PlanApprovalDock.vue` —— 解构并调用它。
- `frontend/src/components/chat/PlanApprovalDock.test.ts` —— 新增回归用例。

**Out of scope**（看起来相关但不许动）：
- `frontend/src/lib/api.ts`、`frontend/src/locales/index.ts` 与任何 i18n 文案。
- `pkg/turn/*`、`pkg/gateway/*` —— 引擎与网关语义由 owner 逐字定稿，不动。
- `frontend/e2e/*` —— 本计划不加浏览器 e2e（owner 2026-10-26 已裁定 CI 不跑 e2e，见 plans/README.md 的 174 REJECTED）。
- `toggleModelPicker` 本身的双态逻辑、按钮顺序、数字键、`reviewAgain` / `handOffTo` 的请求形状。

## Git workflow

- 按 `plans/README.md` 的既有执行约定：**in-tree、零 commit**，改动留在工作区由 owner 审阅。不要自行 `git merge` / `rebase` / `push` / 删 worktree。
- 若 owner 另行要求提交：标题遵循 Conventional Commits（`CONTRIBUTING.md`），如 `fix(web): close the hand-off model picker on Esc`；subject 小写开头、≤100 字符，并 `git commit -s`（DCO）。

## Steps

### Step 1: 在 composable 里加一个「只关不切」的动作

在 `frontend/src/composables/usePendingActions.ts` 中，紧接 `toggleModelPicker` 之后新增：

```ts
/** Esc closes whichever picker is open without changing what it was for:
 *  re-entering toggleModelPicker with the default 'review' would reopen the
 *  list and silently turn a hand-off into a review (plan 289). */
function closeModelPicker(a: ActionRow) {
  modelPicking.value = { ...modelPicking.value, [a.id]: false }
}
```

不要改 `modelPickPurpose` —— 下一次 `toggleModelPicker` 会按 `!modelPicking[a.id] || purpose !== ...` 重新计算，purpose 留着不会造成串台。在 `return { ... }`（`:560` 起，含 `toggleModelPicker` 那一行）里把 `closeModelPicker` 一并导出，位置放在 `toggleModelPicker` 旁边。

**Verify**：`cd frontend && pnpm typecheck` → exit 0、0 errors。

### Step 2: 让 Esc 调用它

在 `frontend/src/components/chat/PlanApprovalDock.vue`：
1. `:259-263` 的解构列表里加入 `closeModelPicker`（与 `toggleModelPicker` 相邻）。
2. 把 `onEsc` 的 `if (picking.value)` 分支从 `toggleModelPicker(props.action)` 改为 `closeModelPicker(props.action)`；其余分支（`feedbackOpen.value` → `refuseAndStop`）与注释保持原样。

改后 `onEsc` 应为：

```ts
async function onEsc() {
  if (picking.value) {
    closeModelPicker(props.action)
    return
  }
  if (feedbackOpen.value) await refuseAndStop(props.action)
}
```

**Verify**：`cd frontend && pnpm typecheck` → exit 0、0 errors；`grep -n 'toggleModelPicker(props.action)$' frontend/src/components/chat/PlanApprovalDock.vue` 无输出（Esc 不再用默认 purpose 重入）。

### Step 3: 补回归用例

在 `frontend/src/components/chat/PlanApprovalDock.test.ts` 的 `describe('PlanApprovalDock')` 内、紧接 `'hands the plan to the row the user picks'` 之后新增一个 `it(...)`，照该用例的 `mountDock` 形状（`handOffAvailable: true`，两行 `planReviewModels`，其中第二行 `id:'row-b'`）。

用例必须覆盖：
1. 点击 `[data-testid="exit-plan-hand-off"]` → `.xp-pick` 出现且 `.xp-pick .cap` 文本是交接标题（用现有断言写法）。
2. 在根节点触发 Esc：`await wrapper.get('[data-testid="exit-plan-approval"]').trigger('keydown', { key: 'Escape' })` → `await nextTick()`。
3. 断言 `wrapper.findAll('.xp-pick')` 长度为 0，且 `store.modelPicking.value['act-exit']` 为 `false`。
4. 在**点击重新打开之前**断言没有发出任何评审/审批请求：本来就用 `vi.spyOn` 建好的 `forebrainApi.actionPlanReview` 与 `forebrainApi.actionsApprove` 此刻都 `not.toHaveBeenCalled()`。（顺序很重要：步骤 5 的点击之后 `actionsApprove` 必然被调用，别再在那里断言「未调用」。）
5. 再次点 `[data-testid="exit-plan-hand-off"]` → 点击第二行 `[data-testid="exit-plan-hand-off-model"]` → 断言 `actionsApprove` 收到 `{ handOffModel: { id: 'row-b', provider: 'zhipuai', model: 'glm-5.3' } }`，且 `actionPlanReview` 仍未被调用（证明 purpose 没漂移成 review）。
6. `wrapper.unmount()`。

**Verify**：`cd frontend && pnpm exec vitest run src/components/chat/PlanApprovalDock.test.ts` → `1 passed (1)`、新增用例出现在输出里；禁止用「全量 `pnpm test` 绿」当证据（它今天以 exit 1 结束，见命令表）。

### Step 4: 收口回归

**Verify**（两条都要跑）：
- `cd frontend && pnpm typecheck` → exit 0、0 errors
- `cd frontend && pnpm build` → exit 0

**不要**把 `pnpm test`（全量）当收口门禁：它今天因 10 个既有的 unhandled rejection 以 exit 1 结束，与本计划无关（见命令表）。若要跑，只记录实际输出。

## Test plan

- 新测试：`frontend/src/components/chat/PlanApprovalDock.test.ts` 里的 `'Esc closes the hand-off picker without turning it into a review'`（名称可用这个），覆盖：交接选择器打开 → Esc 关闭 → 无请求发出 → 重新打开仍是交接语义 → 点行走 `actionsApprove`。
- 结构样板：同文件 `:199-221` 的 `'hands the plan to the row the user picks'`。
- 另需一条更轻的对称性用例（可放同一个 `it` 内）：以 `'review'` 打开选择器后按 Esc 也必须关闭 —— 确认新函数对两种 purpose 都成立。
- 验证：`cd frontend && pnpm exec vitest run src/components/chat/PlanApprovalDock.test.ts` → 全部 PASS，新增用例出现在输出里。

## Done criteria

- [ ] `grep -n 'closeModelPicker' frontend/src/composables/usePendingActions.ts frontend/src/components/chat/PlanApprovalDock.vue` 两个文件都有命中（composable 至少 2 行：定义 + return）。
- [ ] `grep -n 'toggleModelPicker(props.action)$' frontend/src/components/chat/PlanApprovalDock.vue` 无输出。
- [ ] `cd frontend && pnpm exec vitest run src/components/chat/PlanApprovalDock.test.ts` 全 PASS（1 file，含新增用例），exit 0
- [ ] `cd frontend && pnpm typecheck` exit 0；`cd frontend && pnpm build` exit 0。
- [ ] `git status --porcelain -- frontend/src` 只列出 Scope 里的三个文件。
- [ ] `plans/README.md` 中本计划状态行已更新。

## STOP conditions

- Drift check 显示三个文件在 `d715dafd` 之后被改过，且「当前状态」摘录对不上。
- `usePendingActions.ts` 的 `return { ... }` 里找不到 `toggleModelPicker`，或 `modelPicking` / `modelPickPurpose` 不是本计划描述的 ref 形状。
- 修完发现 `pickPurpose` 在别处还有第三个取值（例如 `'goal'`）——说明模型选择器的语义已扩展，先报告再改。
- 新用例必须放宽既有用例的断言才能通过。
- 同一验证连续两次失败。
- 修复看起来需要改 `pkg/turn` / `pkg/gateway` 或任何 i18n 文案。

## Maintenance notes

- 以后若再给同一个选择器加第三种 purpose，Esc 必须继续走 `closeModelPicker` 而不是 `toggleModelPicker` 的默认值；新增 purpose 时把这条回归用例的参数化覆盖一起补上。
- 评审 PR 时重点看：`closeModelPicker` 是否只改 `modelPicking`，以及新用例是否真的断言了「没有请求发出」（只断言 `.xp-pick` 消失不足以挡住 purpose 漂移）。
- 明确延后：浏览器 e2e 覆盖 Esc 路径（owner 已裁定 CI 不跑 e2e）；TUI 不共用这套 composable，无需同步改动。
