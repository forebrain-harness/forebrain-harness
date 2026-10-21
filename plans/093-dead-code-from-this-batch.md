# Plan 093: 清掉这轮改动留下的死代码与死字段（含一个从不挂载、却还带着 6 条绿色测试的组件）

> **Executor instructions**: 按步骤执行，每步跑完「Verify」并确认输出符合预期再进入下一步。
> 命中「STOP conditions」任一条就停下来报告，不要即兴改动。全部完成后更新
> `plans/README.md` 里本计划那一行的状态（除非派发你的评审方说他维护索引）。
>
> **Drift check（先跑）**: `git diff --stat 8d3322c..HEAD -- frontend/src plans/README.md`
> 若有变动，逐条用本计划的 `grep` 命令重新确认"零消费者"再删；对不上按 STOP 处理。

## Status

- **Priority**: P3
- **Effort**: S
- **Risk**: LOW
- **Depends on**: plans/087-approval-card-facts-read-the-wire.md、plans/089-ask-and-queue-affordances-are-real.md（它们会改动本计划要删/要留的部分）
- **Category**: tech-debt
- **Planned at**: commit `8d3322c`, 2026-10-21

## Why this matters

这轮改动留下了一批"看起来还活着"的东西：一个**从不挂载**的审批面板（`PendingActionsPanel.vue`，与 `ApprovalCard.vue`
重复约 200 行渲染，并且已经开始漂移），它自己还带着 6 条永远绿的测试；一个全族都不读的 `ApprovalFacts.path` 字段；
一个无人 `inject` 的 `provide(FileTreeFolderKey, …)`；一个从不 emit 的 `activate` 事件；三对零引用的文案键；
一个零消费者的 `reviewModelLabel`；以及两处"同一表达式写了两遍"。这些不会让功能坏掉，但它们让下一个人（和下一个模型）
以为存在一条通路并去接它。本仓 owner 的规矩是"删掉的语义就当做从来没有存在过"，所以要么让它真正被用，要么删干净。

## Current state

- `frontend/src/components/chat/PendingActionsPanel.vue` — **全仓没有挂载点**：

```
$ grep -rn "PendingActionsPanel" frontend/src --include=*.vue --include=*.ts
frontend/src/components/chat/PendingActionsPanel.test.ts:7,13,37,63,88,118,133,164  （测试自己）
frontend/src/components/chat/ApprovalGateCard.vue:76                                  （注释里提到它）
frontend/src/views/ChatView.test.ts:151                                               （mount 的 stub）
```

  它和 `frontend/src/components/chat/ApprovalCard.vue:23-255` 是逐块重复的渲染，且已经分叉：
  `PendingActionsPanel.vue:98` 仍用 `chat.exitPlanKeepPlanning`（`ApprovalCard.vue:123` 已改成 `chat.exitPlanSendFeedback`）、
  `:125` 与 `:145` 把同一个 gate 错误渲染两遍（`ApprovalGateCard.vue:68` 已经渲染过一次）。
- `frontend/src/lib/approvalGates.ts:34-36` — `path` 字段写入但从不读：

```ts
  /** The file family's path; the thread's call line already names it. */
  path?: string
```

  `ApprovalGateCard.vue` / `ApprovalCard.vue` 都不读 `.path`（`grep -n "facts.path" frontend/src` 零命中）；
  设计稿也明确路径在调用行上（`docs/plan/WEB_REDESIGN_AND_GATEWAY_ACCESS/app-preview.html:3566-3567`：
  「③ 修改文件（edit_file）：目标文件已经在上面那行调用里」）。
- `ai-elements-vue/packages/elements/src/file-tree/FileTreeFolder.vue:34-38` — `provide` 无人消费，且传的是布尔快照：

```ts
provide(FileTreeFolderKey, {
  path: props.path,
  name: props.name,
  isExpanded: isExpanded.value,
})
```

  `grep -rn "FileTreeFolderKey" ai-elements-vue frontend/src` 只命中定义处与这一行。
- `frontend/src/components/chat/WorkspaceRow.vue:105-113,127-130` — 声明并透传 `activate` 事件，但从未 emit
  （`activate()` 只对文件 `emit('insert', …)`，目录直接 return）；因此 `WorkspaceTree.vue:24` 的
  `@activate="onActivate"` 与 `:113-116` 的 `onActivate` 都是不可达代码。
- `frontend/src/components/PendingInputQueuePopover.vue:37,39` — `visible` 与 `hasAny` 是同一个表达式的两份。
- 零引用文案键与导出（均已 `grep` 确认，除 `locales/index.ts` 自身外零命中）：
  `chat.emptyTitle`（zh `:1087` / en `:2839`）、`chat.benchNoRunningAgents`（`:1539` / `:3286`）、
  `chat.exitPlanReviewBy`（`:1631` / `:3378`）、`usePendingActions.ts:102` 的 `reviewModelLabel`（`:499` 仍 return）。
- **不是死代码、是真缺陷**（同一批里做掉）：
  - `frontend/src/locales/index.ts:666` — 中文界面里的英文：`'agents.noLiveAgents': '暂无 live agents'`
    （消费者 `frontend/src/components/AgentRosterPanel.vue:24`）；
  - `ai-elements-vue/packages/elements/src/prompt-input/PromptInputAttachment.vue:60` — 硬编码英文 aria-label：
    `:aria-label="\`Remove ${label}\`"`（同一批改动给 `PromptInputSubmit.vue`、`ConversationScrollButton.vue` 都加了 `label` prop，
    只有它没加）。
  - `pkg/tui/overlays.go:1726-1730` — 空数组参数渲染成只有标签、没有值（对象分支有 `(none)`，数组分支没有）：

```go
	case []any:
		if scalars, ok := approvalScalarList(v); ok {
			return []string{indent + label + ": " + strings.Join(scalars, ", ")}
		}
		return append([]string{indent + label + ":"}, approvalValueLines(v, indent+"  ")...)
```

    而 `approvalScalarList` 对空数组返回 `(nil,false)`（`pkg/tui/overlays.go:1767-1769`）⇒ 输出只有 `"Paths:"`。

## Commands you will need

| Purpose | Command | Expected on success |
|---|---|---|
| 前端单测 | `cd frontend && pnpm test` | 全 passed（文件数应比改动前少 1） |
| 类型检查 | `cd frontend && pnpm exec vue-tsc --noEmit` | exit 0 |
| 库单测 | `cd ai-elements-vue/packages/elements && pnpm test` | 全 passed |
| Go 单包 | `CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -count=1` | `ok` |
| vet | `go vet ./pkg/tui` | 无输出 |
| e2e | `FOREBRAIN_E2E_SPEC="e2e/approval-families.spec.ts e2e/workspace-tree.spec.ts" scripts/acceptance/web_e2e.sh` | `web e2e: PASS` |

## Scope

**In scope**（删除 + 小修）：
- `frontend/src/components/chat/PendingActionsPanel.vue`（**删除整个文件**）
- `frontend/src/components/chat/PendingActionsPanel.test.ts`（**删除整个文件**）
- `frontend/src/views/ChatView.test.ts`（只删 `:151` 的 stub 一行）
- `frontend/src/lib/approvalGates.ts` + `approvalGates.test.ts`（删 `path` 字段及其写入）
- `ai-elements-vue/packages/elements/src/file-tree/FileTreeFolder.vue`（删无人消费的 `provide`）
- `frontend/src/components/chat/WorkspaceRow.vue`、`WorkspaceTree.vue`（删从不 emit 的 `activate` 事件与它的处理函数）
- `frontend/src/components/PendingInputQueuePopover.vue`（`visible`/`hasAny` 合一）
- `frontend/src/composables/usePendingActions.ts`（删 `reviewModelLabel` 及其 `return` 项）
- `frontend/src/locales/index.ts`（删 3 对键；改 `agents.noLiveAgents` 的 zh）
- `ai-elements-vue/packages/elements/src/prompt-input/PromptInputAttachment.vue`（加 `removeLabel?: string` prop）
- `pkg/tui/overlays.go` + `pkg/tui/overlays_test.go`（空数组渲染 `(none)`）

**Out of scope**：
- `frontend/src/components/chat/ApprovalCard.vue`：**保留**，它是唯一在用的审批卡渲染。不要"合并"两个组件。
- `pkg/tui/overlays.go` 里 `approvalFieldLabel` / `approvalValueLines` 的其它分支：只补空数组一处。
- `docs/plan/*/app-preview.html`：不改设计稿。
- 任何 `TODO`/`FIXME` 之外的"顺手清理"：本计划只删上面列出的符号。
- `frontend/src/components/chat/WorkspaceRow.vue` 的键盘/展开语义：091 已定稿，不动。

## Git workflow

- 直接改当前 `dev` 工作区；**不 commit、不 push**，除非派发方明确要求。
- 若要求提交：`refactor(web): drop the unmounted approval panel and the dead fields around it`，
  body 用 `Why:` / `What:` / `Verification:`，`git commit -s`。

## Steps

### Step 1: 删掉从不挂载的审批面板

1. 再确认一次它没有挂载点（命令见「Current state」，输出应当只有测试自身、一条注释、一条 stub）。
2. `rm frontend/src/components/chat/PendingActionsPanel.vue frontend/src/components/chat/PendingActionsPanel.test.ts`。
3. `frontend/src/views/ChatView.test.ts:151` 删掉 `PendingActionsPanel: true,` 这行 stub。
4. 把 `frontend/src/components/chat/ApprovalGateCard.vue:74-78` 的注释里提到 `PendingActionsPanel` 的那半句改成只讲 `ApprovalCard`
   （注释要描述现实）。

**Verify**: `grep -rn "PendingActionsPanel" frontend/src` → 无输出；`cd frontend && pnpm test` → 全 passed。

### Step 2: 删掉全族都不读的 `path` 字段

`frontend/src/lib/approvalGates.ts`：删 `ApprovalFacts` 的 `path?: string`，并删掉 `file_write` / `file_edit` 分支里的
`path: String(payload.resolved_file_path ?? payload.file_path ?? '')` 两行。`protected_read` 的**事实行**用路径是对的
（那是卡片上唯一能看到路径的地方），**不要**动它。

**Verify**: `grep -rn "facts.path\|path?: string" frontend/src/lib/approvalGates.ts frontend/src/components/chat` → 无输出；
`cd frontend && pnpm exec vue-tsc --noEmit` → exit 0。

### Step 3: 删掉未消费的 `provide` 与从不 emit 的事件

1. `FileTreeFolder.vue`：删 `provide(FileTreeFolderKey, {...})` 与对应的 import；若 `context.ts` 里 `FileTreeFolderKey`
   从此无人使用，一并删掉（`grep -rn "FileTreeFolderKey" ai-elements-vue` 确认零命中后再删）。
2. `WorkspaceRow.vue`：从 `defineEmits` 里删 `(e: 'activate', ...)`。
   `WorkspaceTree.vue`：删 `@activate="onActivate"` 这一行绑定与 `onActivate` 函数。
   注意 `WorkspaceRow` 内部**没有**任何 `emit('activate')`，删除不会改变行为；若执行时发现某处 emit 了它，按 STOP 处理。

**Verify**: `grep -rn "'activate'\|\"activate\"\|FileTreeFolderKey" frontend/src ai-elements-vue/packages/elements/src` →
只剩 `frontend/src/components/chat/WorkspaceTree.vue` 里 `@activate` 之外的零命中（或全无）；
`cd frontend && pnpm test` → 全 passed。

### Step 4: 合并重复表达式、删零消费者的导出与文案键

1. `PendingInputQueuePopover.vue`：删 `hasAny`，模板里用 `visible`（`v-if="hasAny"` → `v-if="visible"`）。
2. `usePendingActions.ts`：删 `reviewModelLabel` 函数与 `return { …, reviewModelLabel, … }` 里的那一项。
3. `frontend/src/locales/index.ts` 成对删除：`chat.emptyTitle`、`chat.benchNoRunningAgents`、`chat.exitPlanReviewBy`
   （zh 与 en 各一处，共 6 行）。**每删一个键前**先跑一次
   `grep -rn "<键名>" frontend/src ai-elements-vue | grep -v "locales/index.ts"` ⇒ 必须无输出。

**Verify**: `cd frontend && pnpm test` → 全 passed（语言文件若有"键完整性"用例会在这里报错）。

### Step 5: 两处"真缺陷"小修

1. `frontend/src/locales/index.ts:666`：`'agents.noLiveAgents': '暂无运行中的代理'`（en 不变）。
2. `PromptInputAttachment.vue`：加可选 prop `removeLabel?: string`，`aria-label` 改成
   `props.removeLabel ? \`${props.removeLabel} ${label}\` : \`Remove ${label}\``；调用方（`frontend/src` 里用它的地方，
   `grep -rn "PromptInputAttachment" frontend/src | head`）传 `t('composer.removeAttachment')`（若该键不存在，
   新增 zh「移除附件」/ en「Remove attachment」）。
3. `pkg/tui/overlays.go:1726-1730` 的 `[]any` 分支：`len(v) == 0` 时返回 `indent + label + ": (none)"`（与对象分支一致）。

**Verify**: `CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -run TestApprovalFieldLines -count=1` → `ok`（用例见 Step 6）。

### Step 6: 用例

1. `pkg/tui/overlays_test.go` 加一条：审批载荷含 `"paths": []` ⇒ Explain 里出现 `Paths: (none)`，
   不出现只有 `Paths:` 的行。
2. 前端：Step 4 的删除不应新增用例；但要跑一遍全量确认没有测试依赖被删的键/导出。
   若 `ApprovalCard.test.ts` 或 `usePendingActions.test.ts` 断言了 `reviewModelLabel`，删掉那条断言（不要保留空断言）。

**Verify**: `CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -count=1` → `ok`；`cd frontend && pnpm test` → 全 passed。

### Step 7: 全量回归

**Verify**:
1. `cd frontend && pnpm test` → 全 passed，且测试**文件数**比改动前少 1（删了 panel 的测试文件）。
2. `cd frontend && pnpm exec vue-tsc --noEmit` → exit 0。
3. `cd ai-elements-vue/packages/elements && pnpm test` → 全 passed。
4. `CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -count=1` → `ok`；`go vet ./pkg/tui` → 无输出。
5. `FOREBRAIN_E2E_SPEC="e2e/approval-families.spec.ts e2e/workspace-tree.spec.ts" scripts/acceptance/web_e2e.sh` → `web e2e: PASS`。

## Test plan

- Go：一条空数组用例（Step 6.1）。
- 前端：无新增（本计划是减法），但必须证明没有残留依赖：全量单测 + 类型检查 + 两个 e2e spec。
- 反向验证：删 `PendingActionsPanel.vue` 之前先跑一次 `pnpm test` 记下通过的文件数，删除后再跑，确认只少了那一个文件而不是"少了也没报错"
  （若删文件后仍有测试引用它，Vitest 会直接失败——这本身就是验证）。

## Done criteria

- [ ] `grep -rn "PendingActionsPanel" frontend/src` → 无输出
- [ ] `grep -rn "reviewModelLabel\|chat.exitPlanReviewBy\|chat.emptyTitle\|chat.benchNoRunningAgents" frontend/src` → 无输出
- [ ] `grep -rn "FileTreeFolderKey" ai-elements-vue frontend/src` → 无输出
- [ ] `grep -rn "emit('activate'" frontend/src` → 无输出
- [ ] `grep -n "hasAny" frontend/src/components/PendingInputQueuePopover.vue` → 无输出
- [ ] `grep -n "暂无 live agents" frontend/src/locales/index.ts` → 无输出
- [ ] `grep -n "Remove \${label}" ai-elements-vue/packages/elements/src/prompt-input/PromptInputAttachment.vue` → 只在有 `removeLabel` 时的分支里
- [ ] `cd frontend && pnpm test` / `pnpm exec vue-tsc --noEmit` / 库 `pnpm test` / `go test ./pkg/tui` 全绿
- [ ] `git status --porcelain` 只含 Scope 列出的文件（含两个 `D` 删除）
- [ ] `plans/README.md` 本计划状态行已更新

## STOP conditions

- 任何"零消费者"的 `grep` 出现意外命中（说明该符号已被 087/089 重新用起来）——按实际情况保留，不要强删。
- `PendingActionsPanel` 出现挂载点（工作台或工坊里挂了它）——那它就是活代码，停下来报告，改为"合并两份渲染"的另一种计划。
- 删除某个文案键后全量测试报"键缺失"（说明有动态拼接的用法，`grep` 抓不到）——恢复该键并报告。
- `PromptInputAttachment` 的调用方超过 2 处且各自坐标/文案不同——只加 prop，不改调用方文案，报告差异。
- 任一 Verify 连试两次仍失败。

## Maintenance notes

- **一个语义只有一份实现**：审批族渲染留在 `ApprovalCard.vue` + `ApprovalGateCard.vue`；以后要"工坊里也显示审批"，
  包 `ApprovalCard`，不要复制。
- 受控库组件（`FileTreeFolder`）不提供无消费者的 context：要加交互就在 `FileTree` 的 context 里加**响应式**值。
- 文案键的删除只能在 `grep` 零命中时做；语言文件是全站唯一真相，中文里不要出现英文词（`agents.noLiveAgents` 曾经是反例）。
- 评审要点：删除的文件确实是两个（组件 + 它的测试）；`protected_read` 的路径事实行**没有**被误删；
  e2e 两个 spec 仍通过（说明审批族与文件树没有回退）。
- 后续（本计划不做）：`pkg/tui/overlays.go` 的 `approvalValueLines` 对嵌套空对象/空数组还有同类边界，
  若再出现"只有标签没有值"的报告，按同一规则（`(none)`）补齐。
