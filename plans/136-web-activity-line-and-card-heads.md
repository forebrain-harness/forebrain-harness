# Plan 136：web 活动线、一行卡头（中文化）、眼睛图标 + 抽屉文档页、回到底部

> **执行者须知**：按步骤顺序执行，每步跑验证命令并确认预期结果后再进下一步。触发 "STOP conditions" 时停下报告。
> **不要更新 `plans/README.md`**。**提交**：按 owner 当次派发指令（worktree 必须基于 dev HEAD）；没有指令时不 commit、不 push。
>
> **原型是唯一标准**：`docs/plan/WEB_REDESIGN_AND_GATEWAY_ACCESS/app-preview.html`，用浏览器直接打开，右侧「迭代入口」：
> 「对话 · 全部消息卡（工具 / 思考 / 子代理）」「对话 · shell 命令卡（定稿）」「子代理 · 实时（右侧抽屉）」「技能工坊」各入口；
> 页面顶部「2026-10-25 消息卡重设计（定稿：方案 C）」一节是逐条说明。**1:1 还原**（owner 规则：现有界面与稿不一致也要改）。
> 尺寸、颜色、间距一律从原型 CSS 抄（下文给出选择器名），不要凭感觉。
>
> **组件规则（owner 2026-10-25）**：先用 ai-elements-vue（`@repo/elements`，源码 `ai-elements-vue/packages/elements/src`），
> 再用 shadcn-vue（`@repo/shadcn-vue`，源码 `ai-elements-vue/packages/shadcn-vue/components/ui`），两者都没有对应物才写自定义组件，
> 并在组件注释里写明为什么现成的不行。这两个包就在本仓库，可以改，但改动必须让现有调用方照常工作（跑它们的测试）。
>
> **前置（否则 STOP 1）**：计划 `135` 已落地：`grep -n "func DisplayHeaderParts" pkg/tool/display_header.go`、
> `grep -n "call_header_list" pkg/gateway/api_extra.go`、`test -f pkg/tool/testdata/header_actions.json` 都成立。
>
> **Drift check**：摘录按符号 / 模板里的 `data-testid` 定位（行号写于 dev `1c8f5d9`，仅供参考）；对不上 → STOP 1。

## Status

- **Priority**: P1
- **Effort**: L
- **Risk**: MED（对话、子代理抽屉、技能工坊三处的消息流布局都换）
- **Depends on**: 135
- **Blocks**: 137、138（它们往本计划的卡片骨架里填正文 / 审批底栏）
- **Planned at**: dev `1c8f5d9` + 工作区原型，2026-10-25

## Why this matters（owner 2026-10-25 的定稿，逐条要还原）

1. **活动线**（主视图、子代理抽屉、技能工坊一致）：一个助手回合是一条竖线。线上的节点说明每一块是什么——品牌标识 =
   助手在说话（子代理视图里是机器人图标），大脑 = 思考，勾选清单 = 更新待办，机器人 = 子代理调用，对勾 / 叉号 / 转圈 /
   禁止 / 时钟 / 斜杠圆圈 = 一次调用的状态（时钟 = 等你批准）。卡头行不套框、不重复工具图标；结果面板挂在行下。
   助手的话挂在品牌标识下，正文与卡片内容列对齐，字号大一档（14px / 1.75）。“已工作 …”那一行收尾。
2. **卡头只占一行**：动作词（灰）+ 对象（深色等宽）+ 后缀（灰）；时长单独在右；非 0 退出码在时长左边；最右是折叠箭头。
   放不下就截断加省略号。**中文界面下动作词与后缀一律中文**（owner：「当用户切换到中文时，消息卡头的所有英文文案……
   必须显示成中文」）；英文界面是引擎原话；对象（路径、命令、查询、URL、MCP 参数名）不翻。
3. **眼睛图标**：卡头被截断（或命令有多行）时，标题后出现一个只有眼睛图标的按钮（悬停提示 / 读屏名“显示全部”），点开在
   右侧抽屉里新开一个文档标签页（“命令 · …” / “标题 · …”），与计划标签同一种：复用抽屉的头栏、标签切换、收起。
   shell 显示完整命令（长链按 `&&` / `||` / `|` / `;` 每步一行、连接符在左侧细线上，heredoc 展开成缩进块），其它工具显示完整标题；
   右上角复制。（输出的“输出 · …”标签页在 137。）
4. **正文只放结果**：正文里不再出现完整命令或完整标题（现在 `ToolCallCard` 会把长命令放在正文顶部，要删）。
5. **失败用叉号**（与对勾成对），感叹圆圈只留给页面级错误条。
6. **回到底部**：主视图与子代理视图同一个按钮，离最新内容较远时在输入框（或“工作中”状态行）正上方居中淡入，点了平滑滚到底，
   到底淡出；尊重“减少动态效果”。

## Current state

- 消息流（主视图）：`frontend/src/views/ChatView.vue`，`<Conversation …><ConversationContent>` 内每条消息
  `<Message>` → `<div class="msg-blocks">` → `drawnBlocks(msg.blocks)` 逐块：`ThinkingCard`（`kind==='thinking'`）、
  `MessageResponse`（`'assistant'`）、`ToolCallCard`（`'tool'` 且无 `subagentCall`）、`SubagentCallCard`（`'tool'` 有 subagentCall /
  `'subagent'`）、`ApprovalCard :block`（`'approval'`，已决定的那一行字）、`CompactionCard`、`GoalLine`、`RunErrorBlock`；
  消息尾部 `PlanUpdateCard`（在 `msg-blocks` 之前）、`DiffView`（`msg.turnDiffs`）、`RunWorkedLine`。
  `</ConversationContent>` 之后是 `<ConversationScrollButton :label="t('chat.scrollToLatest')" />`。
- 子代理抽屉：`frontend/src/components/chat/SubagentDrawer.vue`（props 有 `planTabs?: PlanTab[]`、`activePlan`、`order`，事件
  `select-plan`/`close-plan`；默认插槽里放 `PlanDoc` 与 `SubagentTab`）；每个子代理一页 `SubagentTab.vue`（内部
  `<Conversation>…<ConversationScrollButton class="!bottom-[128px]" …/>`），消息流在 `SubagentConversation.vue`
  （`drawnBlocks` 逐块，种类比主视图多 `prompt`、`user`、`plan`、`worked`）。ChatView 用 `planTabs` / `planActive` /
  `drawerTabOrder` / `selectPlanTab` / `closePlanTab` 管理计划标签。
- 技能工坊：`frontend/src/views/WorkshopView.vue`，`message.blocks` 逐块渲染在一个带框的 `max-w-[85%]` 容器里
  （`ThinkingCard` / `MessageResponse` + `CandidateCards` / `ToolCallCard` / `ApprovalCard` / `RunErrorBlock`）。
- 工具卡：`frontend/src/components/chat/ToolCallCard.vue` 用 `@repo/elements/tool` 的 `Tool`（= shadcn `Collapsible`）、`ToolHeader`、
  `ToolContent`、`ToolOutput`。`title` = `step.header`（引擎一句英文）或回退；`fullCommand`：标题长于 120 或含换行时把整条命令
  画在**正文顶部**（`.cmd-full` + 复制按钮）。`TOOL_ICONS` 给每个工具一个图标画在卡头。
- 卡头：`ai-elements-vue/packages/elements/src/tool/ToolHeader.vue`（`CollapsibleTrigger`：图标 + `.tool-head-cmd` 一句话 +
  `StatusBadge` + 折叠箭头；`singleLine` 时 `truncate` + 原生 `title`）。状态图标：`tool/ToolStatusBadge.vue`
  （running = `LoaderIcon`、awaiting = `Clock`、done = `Check`、denied = `Ban`、canceled = `CircleSlash`、其余 = `CircleAlert`）。
  `ToolHeader` 的调用方只有 `ToolCallCard`（`grep -rn "ToolHeader" frontend/src` 确认）。
- 数据：`frontend/src/composables/useChatStream.ts` `export interface SubagentToolStep`（`header?: string`、`status`、`output`、
  `durationSeconds?` …），`toolStepFromPayload(payload, completed, fallbackStepId)`（直播，`payload.header`）、
  `toolStepFromRow(row, call)`（历史，`row.header`）、`canceledToolStep(call, row.callHeaders?.[call.id])`（历史里没跑的调用）。
  响应经 axios 拦截器 `toCamelCase`（`frontend/src/lib/case.ts`，`camelcase-keys` `deep: true`）——**以调用 id 为键的 map 会被改键**，
  所以 135 改发 `call_header_list` 数组。
- 135 之后网关多发：直播 step / 子代理事件 / 历史 tool 行的 `header_parts: {action, target?, suffix?, duration?}`，历史 assistant 行
  `call_header_list: [{call_id, header, parts}]`，`tool_meta.deny_feedback`，shell 的 `shell_card`（137 用）。
  动作词全集在 `pkg/tool/testdata/header_actions.json`。
- 活动线可用的现成组件：`@repo/elements/chain-of-thought` 的 `ChainOfThought`（容器）与 `ChainOfThoughtStep`（左列 `#icon` 插槽 +
  从图标下方画到本步底部的竖线 `<div class="-mx-px absolute top-7 bottom-0 left-1/2 w-px bg-border" />` + 必填的 `label`）。
- 回到底部：`@repo/elements/conversation` 的 `Conversation`（`vue-stick-to-bottom`）与 `ConversationScrollButton`
  （`useStickToBottomContext()` 的 `isAtBottom` / `scrollToBottom`，`absolute bottom-4 left-[50%] size-[30px] … rounded-full`）。
- 文案：`frontend/src/locales/index.ts`（`const zh = {...}`、`const en: Record<keyof typeof zh, string>`、`t(key, params)`、`useI18n()`）。

## Commands you will need

```bash
cd frontend
pnpm vitest run <path or pattern>       # 单测
pnpm typecheck                          # vue-tsc --noEmit，必须 0 错误
pnpm vitest run                         # 收尾全量（与 dev 基线比：只允许基线已有的失败，见 plans/README.md 顶部）
cd .. && FOREBRAIN_E2E_SPEC="e2e/message-cards.spec.ts e2e/subagent-drawer.spec.ts e2e/subagent-conversation.spec.ts e2e/skill-workshop.spec.ts" scripts/acceptance/web_e2e.sh
```
网关侧（Step 1 删字段）：`export PKG_CONFIG_PATH=$PWD/third_party/pdfium/darwin_arm64; CGO_ENABLED=1 go test -tags fts5 ./pkg/gateway/ -count=1`。

## Scope

- 改：`frontend/src/composables/useChatStream.ts`（及其测试）、`frontend/src/lib/api.ts`（行类型）、新建 `frontend/src/locales/toolHeader.ts`（+测试）、
  `frontend/src/components/chat/ToolCallCard.vue`、`ThinkingCard.vue`、`PlanUpdateCard.vue`、`SubagentCallCard.vue`（只动节点相关）、
  新建 `ActivityLine.vue` / `ActivityStep.vue`、新建 `ToolDocCommand.vue` / `ToolDocTitle.vue`、新建 `frontend/src/lib/shellSteps.ts`（+测试）、
  `SubagentDrawer.vue`、`SubagentTab.vue`、`SubagentConversation.vue`、`frontend/src/views/ChatView.vue`、`WorkshopView.vue`、
  `ai-elements-vue/packages/elements/src/tool/ToolHeader.vue`、`ToolStatusBadge.vue`、`chain-of-thought/ChainOfThoughtStep.vue`、
  `conversation/ConversationScrollButton.vue`、对应单测与 e2e；`pkg/gateway/api_extra.go`（只删 `CallHeaders` 字段及其测试断言）。
- **不改**：shell 卡正文 / diff（137）、审批（138）、引擎任何文件、TUI。

## Steps

### Step 1：数据进来（`useChatStream.ts`）

1.1 `SubagentToolStep` 加：
```ts
  /** The header in parts (plan 135): the page lays them out and words the
   *  action in the reader's language. Absent on a payload older than that. */
  headerParts?: HeaderParts
  /** The call's input as the engine recorded it (the full shell command is
   *  input.command); read from the started step or the stored tool meta. */
  input?: Record<string, unknown>
  /** The user's words when they refused this call (tool_meta.deny_feedback). */
  denyFeedback?: string
```
`export interface HeaderParts { action: string; target?: string; suffix?: string; duration?: string }`。
1.2 `toolStepFromPayload`：`payload.headerParts`（对象且 `action` 非空才取）；`input` 取 `payload.input`（直播 started step 有），
`denyFeedback` 取 `metaString(meta, 'deny_feedback')`。注意：完成 step 不带 `input` 时**保留**之前 started step 里的 input
（找到合并 started/completed 的地方——`{ ...block.step, ...step }`——确认 `undefined` 不会覆盖已有值；会覆盖就在构造时省略该键）。
1.3 `toolStepFromRow`：`row.headerParts`；`input` 取 tool meta 的 `input`；`denyFeedback` 同上。
1.4 历史里没跑的调用：`canceledToolStep(call, …)` 改读 `row.callHeaderList`（数组，按 `callId === call.id` 找），把 `header` 与
`parts` 都传进去；`frontend/src/lib/api.ts` 行类型删 `callHeaders`、加 `callHeaderList?: { callId: string; header: string; parts: HeaderParts }[] | null`、
`headerParts?: HeaderParts | null`。
1.5 网关删旧字段：`pkg/gateway/api_extra.go` 历史行的 `CallHeaders` 字段与赋值、`canceledCallHeaders` 函数（若只剩测试在用，测试改测
`canceledCallHeaderList`）。

**Verify**：`useChatStream.test.ts` 新增：直播 payload 带 `headerParts` → step 上有；历史行 `callHeaderList` 里的 `callId: 'call_a_1'` 能对上
调用 `call_a_1`（这正是旧 map 被 camelCase 改键后对不上的回归用例：再加一例断言 `toCamelCase({call_headers: {call_a_1: 'x'}})` 的键已经
不是 `call_a_1`，证明数组是必要的）；`pnpm vitest run src/composables/useChatStream.test.ts`、`go test ./pkg/gateway/` 通过。

### Step 2：卡头的中文词表（新建 `frontend/src/locales/toolHeader.ts`）

```ts
/**
 * Card heads speak the reader's language (owner 2026-10-25). The engine's
 * action is a fixed vocabulary (pkg/tool HeaderActions, golden copy in
 * pkg/tool/testdata/header_actions.json), so the Chinese words come from this
 * table; the target — a path, a command, a query, a URL — is never translated.
 */
export const HEADER_ACTION_ZH: Record<string, string> = { … }
export function headerActionWord(action: string, lang: 'zh' | 'en'): string   // 表里没有 → 原样
export function headerSuffixWords(suffix: string, lang: 'zh' | 'en'): string
```
词表（完成 = “已…”，进行中 = “正在…”，失败 = “…失败”）：

| 引擎 | 中文 | 引擎 | 中文 |
|---|---|---|---|
| Ran / Running / Failed to run | 已运行 / 正在运行 / 运行失败 | Read / Reading / Failed to read | 已读取 / 正在读取 / 读取失败 |
| Edited / Editing / Failed to edit | 已编辑 / 正在编辑 / 编辑失败 | Created / Wrote / Writing / Failed to write | 已创建 / 已写入 / 正在写入 / 写入失败 |
| Fetched / Fetching / Failed to fetch | 已抓取 / 正在抓取 / 抓取失败 | Searched / Searching / Failed to search | 已搜索 / 正在搜索 / 搜索失败 |
| Looked up / Looking up / Failed to look up | 已查询 / 正在查询 / 查询失败 | Requested permissions / Requesting permissions | 已申请权限 / 正在申请权限 |
| Failed to request permissions | 申请权限失败 | Permission request denied / canceled | 权限申请被拒绝 / 权限申请已取消 |
| Retrieved saved output / Retrieving saved output | 已取回保存的输出 / 正在取回保存的输出 | Searched saved output / Searching saved output | 已搜索保存的输出 / 正在搜索保存的输出 |
| Failed to retrieve saved output | 取回保存的输出失败 | Output retrieval denied / canceled | 取回输出被拒绝 / 取回输出已取消 |
| Todo / Updating todo / Failed to update todo | 待办 / 正在更新待办 / 更新待办失败 | Entered / Entering plan mode / Failed to enter plan mode | 已进入计划模式 / 正在进入计划模式 / 进入计划模式失败 |
| Exited plan mode / Failed to exit plan mode / Kept planning | 已退出计划模式 / 退出计划模式失败 / 继续规划 | Saved / Saving memory note / Failed to save memory note | 已保存记忆笔记 / 正在保存记忆笔记 / 保存记忆笔记失败 |
| Memory note save denied / canceled | 保存记忆笔记被拒绝 / 保存记忆笔记已取消 | Listed / Listing memories / Failed to list memories | 已列出记忆 / 正在列出记忆 / 列出记忆失败 |
| Memory listing denied / canceled | 列出记忆被拒绝 / 列出记忆已取消 | Read / Reading memory / Failed to read memory | 已读取记忆 / 正在读取记忆 / 读取记忆失败 |
| Memory read denied / canceled | 读取记忆被拒绝 / 读取记忆已取消 | Searched / Searching memories / Failed to search memories | 已搜索记忆 / 正在搜索记忆 / 搜索记忆失败 |
| Memory search denied / canceled | 搜索记忆被拒绝 / 搜索记忆已取消 | Notes / Saving notes / Failed to save notes | 笔记 / 正在保存笔记 / 保存笔记失败 |
| Asked user / Asking user / Failed to ask user | 已询问用户 / 正在询问用户 / 询问用户失败 | Skill / Loading skill | 技能 / 正在加载技能 |
| Denied / Canceled / MCP | 已拒绝 / 已取消 / MCP | | |

（`header_actions.json` 里若还有表中没有的词，按同样规则补，并在报告里列出。）
后缀：`^lines (.+)$` → `第 $1 行`；`^matching (“.*”)$` → `匹配 $1`；`^in (.+)$`（记忆搜索的范围）→ `在 $1 中`；其它原样（MCP 的
`参数: 值 · …` 是外部工具的字段名，不翻）。

**Verify**：新建 `toolHeader.test.ts`：用 `node:fs` 读 `../../../pkg/tool/testdata/header_actions.json`（路径相对本测试文件，用
`fileURLToPath(new URL(..., import.meta.url))` 求出），**每个**动作在 `HEADER_ACTION_ZH` 里都有非空中文、且不含 ASCII 字母（`MCP` 例外）；
后缀三条规则各一例；英文原样。`pnpm vitest run src/locales/toolHeader.test.ts` 通过。

### Step 3：一行卡头（改 `@repo/elements/tool/ToolHeader.vue`、`ToolStatusBadge.vue`）

3.1 `ToolHeader` 新 props（保留 `title` 作为没有 `parts` 时的回退）：
```ts
  /** The header in parts; when present the head is drawn from them. */
  parts?: { action: string; target?: string; suffix?: string; duration?: string }
  /** The words the parts are drawn in (the action and suffix only). */
  lang?: 'zh' | 'en'
  /** A non-zero exit code, shown left of the duration (shell). */
  exitCode?: number | null
  /** Whether the state is drawn on the activity line's node instead of here. */
  statusOnNode?: boolean
```
并提供一个具名插槽 `#after-title`（放眼睛按钮）。渲染顺序（原型 `.tool-h` 内）：`[ttl: verb | target | suffix] [#after-title] [exit] [duration] [status?] [chevron]`。
- 有 `parts.target`：`<span class="verb">动作词</span><span class="tgt">对象</span><span class="sfx">后缀</span>`；没有对象：整句
  `<span class="solo">动作词</span>`（标题色，不灰）。动作词与后缀经 Step 2 的函数按 `lang` 取词。
- 样式从原型抄：`.tool-h .ttl`、`.ttl .verb`（13px UI 字体，活动线里颜色 `#98a2b3`）、`.ttl .pv.cmdt`/`.tgt`（12px 等宽、`--text`、
  `flex: 0 1 auto`，命令时 `flex: 1 1 0`）、`.ttl .sfx`、`.ttl .solo`、`.tool-h .dur`（12px、`#98a2b3`、tabular-nums）、
  `.shc .tool-h .exit`（红色胶囊“退出码 N”/“Exit N”——这里用 shadcn `Badge`，`variant` 选最接近的再用 class 覆盖颜色）。
  卡头不套底色、不画分隔线（原型 `.al-run > .tool > .tool-h { padding: 6px 0; background: transparent; border-bottom: 0 }`）。
- 不再画工具图标（`icon` prop 保留但默认不画；活动线里节点就是状态）。`statusOnNode` 为真时不画 `StatusBadge`。
- 截断：标题区一行、`text-overflow: ellipsis`；去掉原生 `title` 提示（由眼睛按钮负责看全文）。

3.2 `ToolStatusBadge.vue`：失败图标 `CircleAlert` → `X`（lucide），其余不变；新增 `variant?: 'inline' | 'node'`：`node` 画成原型
`.al-run > .tool > .tool-h > :is(.ok, .bad, .run, .ask, .quiet)` 的 20px 圆（颜色：完成 `#ecfdf3`/`#abefc6` 绿、失败 `#fef3f2`/`#fecdca` 红、
运行与等待 brand-soft/brand-border、拒绝灰、取消 soft；图标 12px、`stroke-width: 2.6`）。

**Verify**：`ToolCallCard.test.ts`（或新建 `ToolHeader.test.ts`，挂在 frontend 下用 `@repo/elements/tool` 导入）：`parts {action:'Read', target:'a.go', suffix:'lines 1-9', duration:'0.3s'}`
在 `lang:'zh'` 下渲染出“已读取”“a.go”“第 1-9 行”“0.3s”，`lang:'en'` 下原样；无对象时只有 `.solo`；`exitCode: 2` 出“退出码 2”；失败状态的图标是 X。

### Step 4：活动线（`ChainOfThought` / `ChainOfThoughtStep` + 两个薄组件）

4.1 改 `@repo/elements/chain-of-thought/ChainOfThoughtStep.vue`（保持现有调用方不变）：`label` 改为可选，没有就不渲染 label 那个 div；
竖线 div 加 `data-slot="chain-of-thought-line"`。（`grep -rn "ChainOfThoughtStep" frontend/src ai-elements-vue/packages/elements/src` 确认其它调用方仍正常。）

4.2 新建 `frontend/src/components/chat/ActivityLine.vue`：包一层 `ChainOfThought`（`class` 覆盖掉 `max-w-prose space-y-4`，改成原型
`.al-run` 的 `display: grid; gap: 10px`），并让**除最后一步外**每一步的竖线向下延伸一个间距（`[data-slot=chain-of-thought-line]` 的
`bottom: -10px`），这样整条线从第一个节点连到最后一个；最后一步的线只在它展开时画到面板底（ChainOfThoughtStep 本来就画到本步底部）。
线色 `var(--line)`、1.5px，节点中心对齐 x=10px（原型 `.al-run::before { left: 10px }`、`.al-node { left: -32px; width: 20px }`，内容列左缩 32px）。

4.3 新建 `ActivityStep.vue`：包 `ChainOfThoughtStep`，prop `node: 'say' | 'sub-say' | 'think' | 'todo' | 'subagent' | { status: ToolStatus }`；
`#icon` 插槽里画节点：`say` = `BrandMark`（`frontend/src/components/BrandMark.vue`，20px）、`sub-say` = 机器人（lucide `Bot`，brand-soft 圆）、
`think` = `Brain`（灰圆）、`todo` = `ListChecks`（brand 圆）、`subagent` = `Bot`、状态 = `ToolStatusBadge variant="node"`。颜色尺寸抄原型
`.al-run > .think > .al-think`、`.al-run > .plan-card > .al-todo`、`.al-say > .al-me`、`.al-say > .al-sub`。默认插槽放卡片本身。

4.4 分组工具 `frontend/src/lib/activityRuns.ts`：
```ts
/** Consecutive blocks that share one activity line; anything else (a compaction
 *  card, a goal line, an error block, a user message) breaks the line. */
export function groupActivityRuns<T extends { kind: string }>(blocks: T[], onLine: (b: T) => boolean): ({ line: true; items: T[] } | { line: false; item: T })[]
```
在线上的种类：`assistant`、`thinking`、`tool`、`subagent`、`plan`（子代理里的待办），以及已决定审批的那一行字（`approval`——放在线上但**不画节点**，
`ActivityStep` 允许 `node: null`）。

4.5 套用到三处：
- `ChatView.vue`：`msg-blocks` 里用 `groupActivityRuns(drawnBlocks(msg.blocks), …)`；线上的块包 `ActivityLine` + `ActivityStep`；
  `MessageResponse`（助手的话）节点 `say`，正文样式抄 `.al-say`（14px / 1.75）；`ThinkingCard` 节点 `think`；`ToolCallCard` 节点 = 它的状态
  （`ToolCallCard` 暴露一个 `statusOf(step)` 纯函数供父组件用，或把节点放进 `ToolCallCard` 自己的根元素——选前者，保持卡片可在线外使用）；
  `SubagentCallCard` 节点 `subagent`；`PlanUpdateCard` 移进这条线的开头，节点 `todo`；`RunWorkedLine` 在线之后、左缩 32px（原型
  `.actline .a > .worked { margin-left: 32px }`）。
- `SubagentConversation.vue`：同样分组；助手的话节点用 `sub-say`（机器人）。
- `WorkshopView.vue`：去掉助手消息外面那个带框的 `max-w-[85%]` 容器，改成同样的活动线（技能工坊 = 会话页超集）；`CandidateCards`
  跟在它所属的助手话后面、在同一步里。
- 各卡片若自己还画了与节点重复的前导图标（例如思考卡的大脑、待办卡的勾选清单），删掉卡内那一个。

**Verify**：新建 `activityRuns.test.ts`（分组规则：连续线上块成一组、`compaction` 打断、空数组）；`ChatView.test.ts`/`SubagentConversation.test.ts`
补断言：一个含思考 + 话 + 两个工具调用的回合渲染出 1 个 `ActivityLine`、4 个 `ActivityStep`，工具步骤的节点是状态图标；
`pnpm vitest run src/views src/components/chat src/lib` 通过。

### Step 5：工具卡（`ToolCallCard.vue`）

- 卡头用 Step 3 的 `ToolHeader`：`:parts="step.headerParts"`、`:title`（回退：`step.header` → 现有回退链）、`:lang`（`useI18n` 的当前语言）、
  `:exit-code`（137 填；本计划先传 `undefined`）、`status-on-node`（在活动线里时；给卡片加 prop `onLine?: boolean`，默认 true，线外调用传 false）。
- 删除 `fullCommand` 计算属性、正文顶部的 `.cmd-full-wrap` 块与 `copyCommand`、`.cmd-full*` 样式（原型：正文只放结果）。
- 卡片外观抄原型 `.al-run > .tool`：卡头无框；正文面板 `border: 1px solid var(--line)`、圆角 10px（`.al-run > .tool > .tool-h + * { margin-top: 2px; … border-radius: 10px }`）。
  `Tool` 根元素的默认边框/圆角（`rounded-lg border`）在活动线里要去掉（用 `class` 覆盖）。
- 眼睛按钮（`#after-title` 插槽）：见 Step 6。
- `TOOL_ICONS` 不再用于卡头（若别处不再引用就删掉）。

**Verify**：`ToolCallCard.test.ts`：长命令（>120 字符、含换行）时正文里**没有** `data-testid="tool-cmd-copy"` 与 `.cmd-full`；卡头只有一行。

### Step 6：眼睛图标 + 抽屉文档页

6.1 截断检测 `frontend/src/composables/useTruncated.ts`：给一个元素 ref，用 `ResizeObserver` 判断 `scrollWidth > clientWidth + 1`，返回 `Ref<boolean>`；
卸载时断开。
6.2 眼睛按钮：shadcn `Button`（`variant="ghost"`，`size="icon"`，24px，用 class 抄原型 `.app .tt-all`：静止 `--muted`、悬停 brand-soft 底 + brand 色），
图标 lucide `Eye` 15px，`aria-label` 与悬停提示 = `t('chat.showAll')`（新增文案：`显示全部` / `Show all`；悬停提示用仓库现有的 `v-tip:top` 指令）。
出现条件：标题区被截断，**或** shell 命令含换行（`String(step.input?.command ?? '').includes('\n')`）。点击 `emit('open-doc', {...})`。
6.3 文档标签数据：`frontend/src/composables/useDocTabs.ts`（或在 ChatView 现有计划标签的状态旁边）：
```ts
export interface DocTab { id: string; kind: 'command' | 'title' | 'output'; label: string; text: string; command?: string; source?: { sessionId: string; agentId?: string; retrieveId?: number } }
```
`id` = `doc:<kind>:<hash(text)>`（同一内容重复点击只聚焦已有标签）。`label`：`命令 · <命令第一行>` / `标题 · <标题>`（`output` 由 137 用）。
6.4 `SubagentDrawer.vue`：照 `planTabs` 的写法加 `docTabs?: DocTab[]`、`activeDoc?: string`、事件 `select-doc` / `close-doc`；标签图标：
`command` = `Terminal`、`title` = `Text`、`output` = `ScrollText`（原型 `tabIcon`）；`order` 里混排。ChatView 管理状态（同 `planTabs` /
`planActive` / `drawerTabOrder`），子代理视图里的卡片点开也开在这同一个抽屉（`SubagentConversation` → `SubagentTab` → `SubagentDrawer`
→ ChatView 冒泡 `open-doc`）。
6.5 文档页内容：
- `ToolDocCommand.vue`：把 `shellSteps(command)`（下面 6.6）的结果画成原型的 `.steps`：每步一行，`.steps.multi` 时左侧 22px 一列放连接符
  （brand 色、600、10.5px 等宽，右侧一条 brand-border 细线），heredoc 体是 `.hd-body` 缩进块；只用灰阶 + 字符串一个暖色（`#93600c`）+ 变量一个
  冷色（`#0e6e7e`）+ 命令名加粗（原型 `.shc .k-*`）。右上角复制按钮：复制的是**原始命令文本**（不是排版后的）。
- `ToolDocTitle.vue`：完整标题一段 + 复制。
- 复制按钮：优先 ai-elements（`code-block` 的 `CodeBlockCopyButton` 需要 `CodeBlock` 上下文、会套 Shiki 主题，与原型的纯灰阶排版冲突——
  所以用 shadcn `Button` + lucide `Copy`/`Check` 两态，组件注释写明这个原因）。
6.6 `frontend/src/lib/shellSteps.ts`：把原型 `shSplit` / `shHi` / `shSteps`（`app-preview.html` 里同名函数）移植成 TS，返回结构化数据
`{ op: string; tokens: {kind: 'cmd'|'flag'|'str'|'var'|'op'|'text', text: string}[] }[]` 与 heredoc 块，**不要**拼 HTML 字符串（Vue 模板渲染，避免 XSS）。
规则照抄：一行超过 56 个字符才按顶层 `&&` `||` `|` `;` 拆（引号内不拆）；`<<'EOF'` / `<<EOF` / `<<-EOF` 的体收成一个块直到结束标记行。

**Verify**：`shellSteps.test.ts`（原型的长链、引号里的 `|` 不拆、heredoc、`sudo` 与 `VAR=x` 前缀不算命令名）；`SubagentDrawer.test.ts` 新增：
打开一个 `command` 文档标签 → 标签名“命令 · …”、内容区有步骤行、复制按钮写入原始命令；`ToolCallCard.test.ts`：被截断时出现眼睛按钮
（用 `ResizeObserver` 的测试替身把宽度设小），点击 emit `open-doc`。

### Step 7：回到底部（`ConversationScrollButton`）

- 位置：主视图的按钮已在 `Conversation` 内、底部 `bottom-4`；子代理是 `!bottom-[128px]`。都改成原型：在输入框（或“工作中”状态行）**正上方 12px**、
  水平居中（子代理那页去掉 128px 的特殊值，按它自己的消息区底边定位——若子代理页的输入框在 `Conversation` 之外，`bottom-3` 即可；在之内则按其高度算，
  实测与原型对齐）。
- 出现与消失：用 Vue `<Transition>`：淡入 + 上移 6px、160ms（原型 `.sa-down` / `.sa-down.on`），`@media (prefers-reduced-motion: reduce)` 下无动画；
  点击 `scrollToBottom()`（库自带平滑滚动；reduced-motion 时用 `'instant'`，若库支持该参数）。悬停图标变 brand 色。
- 阈值：原型是“离底部超过 120px 出现”。`vue-stick-to-bottom` 若提供阈值/偏移参数就设 120；没有就保持库的 `isAtBottom`，在报告里写明。

**Verify**：`SubagentTab.test.ts` 现有 stub 仍能用；新增一个 `ConversationScrollButton` 单测（`isAtBottom=false` 时渲染、`true` 时不渲染、点击调用
`scrollToBottom`）。

### Step 8：e2e 与真机

- 更新 `frontend/e2e/message-cards.spec.ts`：它现在断言“长命令移进正文”——改为断言卡头一行、正文没有命令、出现眼睛按钮、点开抽屉标签“命令 · …”；
  中文界面下卡头动作词是中文（例如“已读取”“已运行”），切英文后是 `Read` / `Ran`。
- `subagent-drawer.spec.ts` / `subagent-conversation.spec.ts`：子代理里的卡片点眼睛 → 主抽屉多一个文档标签；活动线节点存在。
- `skill-workshop.spec.ts`：工坊消息走活动线（不再有外框）。
- 跑：`FOREBRAIN_E2E_SPEC="e2e/message-cards.spec.ts e2e/subagent-drawer.spec.ts e2e/subagent-conversation.spec.ts e2e/skill-workshop.spec.ts" scripts/acceptance/web_e2e.sh`。
- 截图对照原型：把 e2e 截图（`shot(...)`）与原型同一入口的截图并排放进报告（主视图一屏、子代理抽屉一屏、工坊一屏、中文卡头一屏），逐项说明差异；
  有差异就改到一致。

## Done criteria

- [ ] 三处消息流都是活动线；节点种类与原型一致；助手的话 14px / 1.75
- [ ] 卡头一行；中文界面动作词与后缀全中文（`toolHeader.test.ts` 覆盖 `header_actions.json` 每个词），英文界面是引擎原话
- [ ] 失败图标是 X
- [ ] 截断或多行命令出现眼睛按钮，点开抽屉文档标签，复制得到原文
- [ ] 正文不再出现命令
- [ ] 回到底部在两个视图位置、动效一致
- [ ] `call_headers` 已从网关与 web 删除，没跑的调用卡头来自 `call_header_list`
- [ ] `pnpm typecheck` 0 错误；`pnpm vitest run` 无新增失败；上面四个 e2e 通过；`go test ./pkg/gateway/` 通过
- [ ] 报告附原型 vs 实现截图对照

## STOP conditions

1. 前置（135）未落地或锚点对不上。
2. `ChainOfThoughtStep` 的结构无法在不破坏现有调用方的前提下做成活动线（例如竖线必须由父容器测量才能连续）——回报现有调用方清单与原因，不要自己另写一套竖线。
3. `ToolHeader` 改动后有其它调用方坏了（本计划写作时只有 `ToolCallCard`）。
4. 子代理视图的卡片点开文档标签需要跨越的组件层次与上文不同、无法冒泡到 ChatView——回报实际层次。
5. 某一步验证连续两次修正后仍失败。

## Maintenance notes

- 新增卡头动作词：引擎 `HeaderActions()` + `header_actions.json` + 这里的 `HEADER_ACTION_ZH` 三处一起改（测试会红）。
- 任何新的消息块种类都要决定“在不在活动线上、节点是什么”，并加进 `groupActivityRuns` 的判断与 `ActivityStep` 的节点表。
- 抽屉的文档标签是通用机制：以后的“看全文”类需求（例如 137 的完整输出）都走 `DocTab`，不要另开弹窗。
