# Plan 137：web shell 卡正文、全站统一的 diff、“输出”文档页

> **执行者须知**：按步骤顺序执行，每步跑验证命令并确认预期结果后再进下一步。触发 "STOP conditions" 时停下报告。
> **不要更新 `plans/README.md`**。**提交**：按 owner 当次派发指令（worktree 必须基于 dev HEAD）；没有指令时不 commit、不 push。
>
> **原型是唯一标准**：`docs/plan/WEB_REDESIGN_AND_GATEWAY_ACCESS/app-preview.html`，入口「对话 · shell 命令卡（定稿）」（逐张看 shell 卡的
> 各种情况）、「对话 · 全部消息卡」（已编辑 / 已创建卡）、「对话 · 审批卡（全部工具）」（补丁的文件列表）；页面顶部「2026-10-25 消息卡重设计」
> 一节的「输出」「改动的文件」「diff 全站统一」三条是逐条说明。尺寸颜色从原型 CSS 抄（下文给选择器名）。1:1 还原。
>
> **组件规则（owner 2026-10-25）**：先 ai-elements-vue（`@repo/elements`），再 shadcn-vue（`@repo/shadcn-vue`），都没有才自写，并在注释写明原因。
>
> **前置（否则 STOP 1）**：`135`、`136` 已落地：`grep -n "func ParseShellCard\|func ParseFileDiff" pkg/tool/shell_card.go`、
> `grep -n "tool-output/:retrieve" pkg/gateway/api_extra.go`、`test -f frontend/src/components/chat/ActivityLine.vue`、
> `grep -n "DocTab" frontend/src/components/chat/SubagentDrawer.vue` 都有结果。
>
> **Drift check**：按符号定位；对不上 → STOP 1。

## Status

- **Priority**: P1
- **Effort**: M–L
- **Risk**: MED
- **Depends on**: 135、136（以及 135 的前置 130、132）
- **Planned at**: dev `1c8f5d9` + 工作区原型，2026-10-25

## Why this matters（owner 2026-10-25 定稿）

- **shell 卡正文只放结果**：输出（stdout 灰阶等宽，stderr 红色）；没有输出写“没有输出”（有文件改动时不写）；鼠标停在输出上右上角出现复制按钮；
  长输出在面板内滚动（360px），**没有**“显示其余 N 行 / 收起”；运行中实时显示最后几行、末尾一个光标，更早的行写“前面 N 行已折叠”。
  被截断时底栏写“中间 412 KB 已省略”，后面是眼睛图标（悬停“查看完整输出”），点开在右侧抽屉新开“输出 · 命令”文档页，显示完整输出、可复制。
  非 0 退出码在卡头时长左边（136 的 `ToolHeader` 已留 `exitCode`）。**不再出现**：`**shell**`、`input:` / `output:` JSON、`stdout:` / `stderr:` 标签、
  沙箱元数据、`$` 提示符、彩色语法高亮。
- **改动的文件**（计划 130 的数据）：每个文件一行——新建 / 修改 / 删除用小色块图标区分，目录灰、文件名深，路径太长从目录那头省略、保住文件名，
  右侧 +N −M；之后是 diff；最后是几句短注（还有 N 个文件改动、哪些文件没法显示、同一仓库有并发命令、git 切换分支不比较文件）。
- **diff 全站统一**：shell 卡的改动文件、已编辑卡、已创建卡、审批卡的预览、补丁的文件列表、消息末尾的回合 diff、工作台“变更文件”、技能工坊的对比，
  都用同一种：左侧 3px 色条 + 浅底色区分增删，行号很淡（一列），新建的文件按普通代码显示（不整片铺绿）。

## Current state

- `frontend/src/components/chat/ToolCallCard.vue` 正文：`<ToolOutput :output="step.output ?? ''" :error-text="…" />`；
  `@repo/elements/tool/ToolOutput.vue`：字符串输出用 `MessageResponse` 当 Markdown 渲染（所以 shell 卡现在显示 `**shell**`、`input:` JSON、
  `stdout:` 围栏、`output:` JSON），`.tool-body { max-height: 208px; overflow: auto }`。
- 运行中的输出：`frontend/src/composables/useChatStream.ts` 的 `case 'tool_output_delta'` 把 `payload.text` 追加到 `step.output`
  （主视图与子代理各一处；完成时 `toolStepFromPayload` 用 `displayBody` 覆盖）。
- 135 之后网关在 shell 行上发 `shell_card`：`{stdout, stderr, exit_code?, stdout_omitted_bytes, stderr_omitted_bytes, omitted_bytes,
  retrieve_id, spool_omitted_bytes, files: [{verb, path, added, deleted, unified_diff}], notes: []}`（经 `toCamelCase` 后为 camelCase）；
  edit_file / write_file 行发 `file_diff`：`{path, added, deleted, unified_diff, rest}`；完整输出接口
  `GET /api/chat/sessions/:id/tool-output/:retrieve` 与 `GET /api/chat/sessions/:id/subagents/:agent/tool-output/:retrieve` →
  `{command, text, spool_omitted_bytes}`。
- 现有 diff 渲染（都要换成统一样式）：`frontend/src/components/DiffView.vue`（ChatView 消息末尾 `msg.turnDiffs`，表格、新旧两列行号、
  `diff-add-label`）；`frontend/src/components/workbench/ChangedFilesTab.vue`（“对比”弹窗 `.chg-diff`，逐行 `diffLineClass(line)`）；
  `frontend/src/components/chat/ApprovalGateCard.vue` 的 `.gate-diff`（**由 138 处理**，本计划不动）；技能工坊的版本对比（`grep -rn "diff" frontend/src/components/workshop` 找到渲染处）。
- 136 之后：`ToolCallCard` 在活动线上，卡头 `ToolHeader` 有 `exitCode` prop；抽屉有通用 `DocTab`（`kind: 'output'` 已预留，`source?: {sessionId, agentId?, retrieveId?}`）。

## Commands you will need

```bash
cd frontend && pnpm vitest run <pattern> && pnpm typecheck
cd .. && FOREBRAIN_E2E_SPEC="e2e/message-cards.spec.ts e2e/workbench.spec.ts e2e/skill-workshop.spec.ts" scripts/acceptance/web_e2e.sh
```

## Scope

- 新建：`frontend/src/lib/unifiedDiff.ts`（+测试）、`frontend/src/components/chat/DiffBlock.vue`、`FileChangeList.vue`、`ShellCardBody.vue`、
  `ToolDocOutput.vue`（+各自测试）。
- 改：`useChatStream.ts`（类型与取数）、`frontend/src/lib/api.ts`（两个读完整输出的函数、行类型）、`ToolCallCard.vue`、`SubagentDrawer.vue`（挂 output 文档页）、
  `ChatView.vue`（打开 output 文档页）、`DiffView.vue`、`ChangedFilesTab.vue`、技能工坊里的 diff 渲染处、locales、e2e。
- **不改**：引擎与网关、`ApprovalGateCard.vue`（138）、卡头与活动线（136 已做）。
- 注意命名：lucide 有个图标叫 `FileDiff`（`ChangedFilesTab.vue` 在用），新组件叫 `DiffBlock`，不要叫 `FileDiff`。

## Steps

### Step 1：数据（`useChatStream.ts`、`api.ts`）

`SubagentToolStep` 加：
```ts
  /** A shell call's card facts (plan 135), from the gateway. */
  shellCard?: ShellCard
  /** A file tool's diff (plan 135): edit_file / write_file. */
  fileDiff?: { path: string; added: number; deleted: number; unifiedDiff?: string; rest?: string }
```
`ShellCard` 照 135 的字段（camelCase）。`toolStepFromPayload` 取 `payload.shellCard` / `payload.fileDiff`；`toolStepFromRow` 取 `row.shellCard` / `row.fileDiff`。
`api.ts` 加：
```ts
  chatToolOutput(sessionId: string, retrieveId: number): Promise<{ command: string; text: string; spoolOmittedBytes: number }>
  subagentToolOutput(sessionId: string, agentId: string, retrieveId: number): Promise<…same…>
```
（照 `chatMessages` 的写法用 `api.get`，路径里的参数 `encodeURIComponent`。）
**Verify**：`useChatStream.test.ts` 加两例（直播与历史行都能取到 `shellCard`、`fileDiff`）。

### Step 2：统一 diff 的数据与组件

2.1 `frontend/src/lib/unifiedDiff.ts`：
```ts
export type DiffRow = { mark: ' ' | '+' | '-' | '…'; line: number | null; text: string }
/** parseUnifiedDiff turns a unified diff (git's, or the engine's turn diff)
 *  into the rows a card draws: context and added lines carry the new file's
 *  line number, removed lines the old file's, and the break between two
 *  hunks is one '…' row. Header lines (diff --git, index, ---, +++) are dropped. */
export function parseUnifiedDiff(text: string): DiffRow[]
```
`@@ -a,b +c,d @@` 起算行号；`\ No newline at end of file` 丢弃；第一个 hunk 之前不插 `…`。
2.2 `DiffBlock.vue`：props `rows: DiffRow[]`、`plain?: boolean`（新建文件：全部按上下文行画，不铺绿）、`maxHeight?: number`（默认 360）。
渲染照原型 `:is(.shc, .fdiff) .fc-b` / `.dr`：底色 `#fbfbfc`、上边线、`padding: 6px 0 10px`、11.5px/1.65 等宽；每行 `grid-template-columns: 52px minmax(0,1fr)`，
行号 `#b0b7c3`、右对齐、`user-select: none`；`add` 行 `#edfbf2` + `inset 3px 0 0 #32d583`；`del` 行 `#fff1f0` + `inset 3px 0 0 #f97066`、文字 `#7a271a`；
`…` 行灰；`white-space: pre-wrap; overflow-wrap: anywhere; tab-size: 4`；增删行前放读屏文字“新增 / 删除”（`sr-only`）。
滚动：用 shadcn `ScrollArea`（`max-h-[360px]`）。
2.3 `FileChangeList.vue`：props `files: {verb, path, added, deleted, unifiedDiff?}[]`、`notes?: string[]`；每个文件一行（原型 `.fc` / `.fc-h`）：
20px 圆角色块图标（新建 = `Plus` 绿、修改 = `Pencil` brand、删除 = `Minus` 红，悬停提示与读屏名“新建 / 修改 / 删除”），路径 = 目录（灰，`direction: rtl`
从左省略、内部 `<bdi>`）+ 文件名（深、500），右侧 `+N`（绿）`−M`（红）；有 diff 时下面一个 `DiffBlock`（新建文件 `plain`）；注一行一条（原型 `.fc-note`，
前面一个灰色小图标：更多文件 = `FilePlus2`/`Files`、不可用 = `FileX`、并发 = `GitMerge`、跳过 / 切分支 = `GitBranch`——按注的文案前缀选，写一个映射表）。
注的文案：135 给的是引擎原句（英文）。中文界面下按原型的中文显示：在 `frontend/src/locales/toolHeader.ts` 旁新建 `diffNotes.ts`，把计划 130 的注
（`(file diff skipped for this git command)`、`(another command ran in this repository at the same time; …)`、`(a git step in this command can move the working tree, …)`、
`… N more file(s) changed`、`N file(s) changed (binary, mode only or too large to show)`、`(file diff unavailable for this command[; N file(s) changed])`，
以及末尾可能的 ` (part of the diff is unavailable)`）用正则翻成：`git 切换分支类命令，不比较文件`、`同一仓库同时有别的命令在跑，改动可能记在任一条下`、
`命令里有 git 步骤，可能是它移动了工作区`、`还有 N 个文件改动`、`N 个文件改动（二进制、仅权限或太大，不显示）`、`这条命令的文件差异不可用（N 个文件改动）`、`（部分差异不可用）`；
没匹配上的原样显示。

**Verify**：`unifiedDiff.test.ts`（两个 hunk 的行号与 `…`、只有新增、删除行号取旧文件）；`DiffBlock.test.ts`（`plain` 时没有 add 类；读屏文字存在）；
`FileChangeList.test.ts`（三种状态图标、长路径的目录与文件名分开、注的中文翻译与未知注原样）。

### Step 3：shell 卡正文（`ShellCardBody.vue`）

props：`card?: ShellCard`、`status: ToolStatus`、`liveOutput?: string`（运行中累计的输出）、`command: string`、`docSource?: {sessionId, agentId?}`；emit `open-doc`。
- **运行中**（`status === 'running'`）：取 `liveOutput` 的最后 4 行（原型 demo 就是 4 行），更早的行数 > 0 时先写一行“前面 N 行已折叠”；最后一行末尾一个光标
  （原型 `.shc .caret`：brand 色小方块、1.1s 闪烁；reduced-motion 下不闪）。
- **完成**：输出面板（原型 `.shc .outz`：上边线、`padding: 10px 44px 12px 14px`、12px/1.65 等宽、`--text-2`）：stdout 每行一个 `.ol`，stderr 每行 `.ol.err`
  （`#b42318`），放在 shadcn `ScrollArea`（360px）里；没有 stdout 也没有 stderr：有文件改动时不画面板，否则画一行“没有输出”（`.none`，12.5px UI 字体 `#98a2b3`）。
  复制按钮：鼠标停在面板上或键盘聚焦时出现（原型 `.shc .cp`，28px，右上角 8px），复制 stdout（有 stderr 时 stdout + 换行 + stderr），复制后对勾 1.2 秒；
  用 shadcn `Button`（ghost、icon），lucide `Copy`/`Check`。
- **改动的文件**：`FileChangeList :files="card.files" :notes="card.notes"`。
- **底栏**（原型 `.shc .foot`：上边线、`padding: 9px 14px`、12px、`--muted`）：被截断时（`stdoutOmittedBytes + stderrOmittedBytes`，或 `omittedBytes`，> 0）写
  `中间 <size> 已省略`（`size` 按 1024 进制：`412 KB`、`3.4 MB`，一位小数仅在 < 10 时保留；英文 `<size> omitted from the middle`）；`retrieveId > 0` 时后面跟
  眼睛按钮（与 136 的眼睛同一组件 / 同一样式），悬停与读屏名 `t('chat.openFullOutput')`（新增：`查看完整输出` / `Open the full output`），点击 emit
  `open-doc {kind: 'output', label: '输出 · <命令第一行>', text: '', command, source: {...docSource, retrieveId}}`。
- 不渲染 `card` 里没有的任何东西；**绝不**回退去渲染 `step.output` 的 Markdown（那正是要去掉的东西）。只有 `card` 缺失（135 之前的老数据且正文解析失败）
  时，`ToolCallCard` 才回退到现有 `ToolOutput`。

### Step 4：接进工具卡（`ToolCallCard.vue`）

- 工具名 `shell` 且（`step.shellCard` 存在或状态是运行中）→ 正文用 `ShellCardBody`（`liveOutput` 传运行中的 `step.output`）；卡头 `exitCode` 传
  `step.shellCard?.exitCode`（非 0 才显示，0 / 缺失传 `null`）。
- `step.fileDiff` 存在 → 正文 = `DiffBlock :rows="parseUnifiedDiff(fileDiff.unifiedDiff)"`（工具名 `write_file` 且卡头动作是 `Created` 时 `plain`），
  `fileDiff.rest` 非空时下面接 `ToolOutput :output="fileDiff.rest"`（诊断等照旧 Markdown）。
- 其余照旧 `ToolOutput`；但 `ToolOutput` 的 `.tool-body { max-height: 208px }` 改成 360px（原型统一的面板滚动高度），面板样式与活动线一致。
- `open-doc` 往上冒泡（136 已把卡片的 `open-doc` 接到 ChatView）。

### Step 5：“输出 · …”文档页（`ToolDocOutput.vue`）

- 打开时按 `source` 调 `chatToolOutput` 或 `subagentToolOutput`；加载中显示一行“正在读取完整输出…”，失败显示错误一行（`--danger`），404 显示“完整输出已不在了”
  （新增文案，中英各一）。
- 正文：`<pre>`（12px/1.65 等宽、`white-space: pre-wrap`）放在 shadcn `ScrollArea` 里填满抽屉内容区；右上角复制（复制全文）。
- 大输出：一次只渲染前 2 MB，滚到底部附近（IntersectionObserver 看一个底部哨兵）再追加下一个 2 MB，直到全部；不出现按钮。
- `spoolOmittedBytes > 0`：文末一行灰字 `超过 64 MB 的部分没有保存` / `Output past 64 MB was not kept`（owner 2026-10-25 已确认文案）。
- `SubagentDrawer`：`DocTab.kind === 'output'` 时内容区渲染 `ToolDocOutput`（136 已有 command / title 的分支，照着加）。

**Verify**：`ToolDocOutput.test.ts`（mock 两个 api：正文、404 文案、2 MB 分段在哨兵可见时追加、spool 注）；`ShellCardBody.test.ts`：
① 只有 stdout、② stdout + stderr（stderr 有 `.err`）、③ 无输出无文件 → “没有输出”、④ 无输出有文件 → 不画面板、⑤ 截断 + `retrieveId` → 底栏文案与眼睛、点眼睛 emit
`open-doc` 且 `source.retrieveId` 正确、⑥ 运行中 10 行 → “前面 6 行已折叠” + 4 行 + 光标；`ToolCallCard.test.ts`：shell 卡正文里**没有** `**shell**`、`input:`、`output:`、`"sandbox_backend"`。

### Step 6：其余 diff 换成统一样式

- `DiffView.vue`（消息末尾的回合 diff）：保留按文件折叠与自动展开阈值，文件头改成 `FileChangeList` 的那一行样式，hunk 内容改用 `DiffBlock`
  （它的 `files[].hunks[].lines[]` 已有 `kind` / `new_no` / `old_no`，直接映射成 `DiffRow`，不要再拼文本重解析）；删掉两列行号表格与旧的颜色类。
- `ChangedFilesTab.vue` 的“对比”弹窗：`diffLines` 改为 `parseUnifiedDiff(全文)` + `DiffBlock`（弹窗内不限 360px 高度：`maxHeight` 传弹窗可用高度或不限）。
- 技能工坊里的版本对比：同样改用 `parseUnifiedDiff` + `DiffBlock`（若那里的数据不是统一 diff 格式，而是逐行 `+`/`-` 文本，就写一个从这种文本到 `DiffRow`
  的小转换，行号为空——原型里这类对比没有行号）。
- 全仓再搜一遍旧样式：`grep -rn "diff-add\|diff-del\|chg-diff\|class=\"add\"\|class=\"del\"" frontend/src`，除 `ApprovalGateCard`（138）外都换掉。

**Verify**：`DiffView.test.ts`、`ChangedFilesTab` 相关测试按新结构更新后通过；`pnpm typecheck` 0 错误。

### Step 7：e2e 与截图对照

- `message-cards.spec.ts`：用它现有的“直接往 state 库写行”的方式，加一条 shell 行（正文由 `formatShellStep` 的格式写成：stdout + stderr + `preview truncated: stdout omitted 421888 bytes`
  + `output:` JSON 带 `retrieve_id`、`exit_code: 1`）与一条 edit_file 行（`turn diff:` 段）：断言 shell 卡正文没有 `**shell**` / JSON、stderr 红、卡头“退出码 1”、底栏“中间 412 KB 已省略” + 眼睛；
  edit 卡是 `DiffBlock`（有 `新增` 读屏文字）。完整输出：再往 output store 里写一条记录有困难的话，用 `page.route` 拦截 `tool-output` 接口返回固定文本，断言抽屉标签“输出 · …”与正文。
- `workbench.spec.ts`：对比弹窗是新样式。
- 截图：原型「对话 · shell 命令卡（定稿）」11 张卡对应的实现截图并排放进报告，逐项说明差异并改到一致。

## Done criteria

- [ ] shell 卡正文只有输出 / 改动文件 / 注 / 底栏，没有任何 JSON、标签、`**shell**`
- [ ] 运行中显示最后 4 行 + 折叠行数 + 光标；完成后 360px 内滚动，没有“显示其余 / 收起”
- [ ] 截断底栏 + 眼睛 → 抽屉“输出 · …”页能读到完整输出（主代理与子代理各一）
- [ ] 已编辑 / 已创建 / 回合 diff / 工作台对比 / 工坊对比都是统一 diff 样式；新建文件不铺绿
- [ ] 中文界面下 diff 的短注是中文
- [ ] `pnpm typecheck` 0；`pnpm vitest run` 无新增失败；上面三个 e2e 通过；报告附原型对照截图

## STOP conditions

1. 前置（135、136）未落地或锚点对不上。
2. 某种卡的数据在 135 的字段里拿不到（例如老行没有 `shell_card` 且正文也解析不出）而原型要求显示——回报行的样子，不要回退去渲染 Markdown 正文。
3. 技能工坊的对比数据既不是统一 diff 也不是逐行 `+`/`-` 文本。
4. 完整输出接口在 e2e 里对 10 MB 以上的文本让页面明显卡顿（>1 秒无响应）——回报实测，不要自己改成按钮分页。
5. 某一步验证连续两次修正后仍失败。

## Maintenance notes

- `DiffBlock` 是全站唯一的 diff 行组件；以后任何显示 diff 的地方都用它，不要再写一套颜色。
- 计划 130 的注文案若改动，同步 `diffNotes.ts` 的正则与测试。
- `ShellCardBody` 只认 `shellCard`：正文格式（`formatShellStep`）的任何变化都应先在 135 的 `ParseShellCard` 里接住，web 不解析 Markdown。
