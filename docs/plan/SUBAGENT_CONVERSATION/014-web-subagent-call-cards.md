# 计划 014：网页——与 TUI 对齐的 subagent_* 卡片

> **执行者须知**：逐步执行。每一步都运行"验证"命令并确认结果后再进入下一步。出现"STOP 条件"里的任何情况，立即停止并报告。
> 完成后更新 `docs/plan/SUBAGENT_CONVERSATION/README.md` 里本计划的状态行。**不要提交代码。** 先读 README 的"全局规则"（尤其第 8、10 条）和"subagent_* 工具卡片审计"一节。
>
> **前置检查**：README 里计划 010、012、013 必须是 `DONE`；`docs/plan/SUBAGENT_CONVERSATION/subagent-cards-preview.html` 的"网页"页已经 owner 确认（README 决策 D12 不是"待定"）。否则 STOP。TUI 是语义标准：卡片说什么、何时变化，以 013 落地后的 TUI 为准；本计划只做网页的呈现。
>
> **漂移检查（先运行）**：
> `git diff --stat bda9505 -- frontend/src/composables/useChatStream.ts frontend/src/views/ChatView.vue frontend/src/components/chat/SubagentCard.vue frontend/src/components/chat/SubagentConversation.vue frontend/src/components/chat/ToolCallCard.vue frontend/src/locales/index.ts frontend/src/lib/api.ts pkg/gateway/api_extra.go`
> 计划 008、010 会改其中若干文件，那是预期的；按函数名和注释原文核对"现状"摘录，对不上就 STOP。

## 状态

- **优先级**：P1
- **工作量**：L
- **风险**：MED（`useChatStream.ts` 的 subagent 事件处理与时间线结构都要动）
- **依赖**：010、012、013、015（plan review 卡片用 015 的事件；008 若已完成，按它落地后的代码核对；008 未做也不阻塞本计划）；W11 的收尾由 SRT-004 提供，本计划不等它
- **类别**：bug
- **基线**：提交 `bda9505`，2026-10-05

## owner 的要求（逐字）

> 必须全面审计所有的subagent_*工具的消息卡片的UI和UX，找出所有问题，定位根因并设计修复计划，加入本期计划中

> （2026-10-05，看过预览后）把 · 66 tool uses 这行统计删掉

> ✓ 任务16 migrate 导入、✗ 任务17 扩展目录和计划002 Web车道实施这样的任务名称后面加上运行时长计时和最终耗时
>
> 运行中的加上运行时长计时，执行结束的（包括成功、失败、取消等）的加上最终耗时

（owner 的长期规矩：网页与 TUI 的内容和数据 1:1；网页不得把工具结果一刀切显示成 JSON；网页的界面改动先给 owner 看可点击的预览，确认后才写代码。）

## 审计发现（网页，逐条，含根因）

| # | 现象 | 根因（`bda9505`） |
| --- | --- | --- |
| W1 | 六个生命周期工具的卡片**标题**就是原始 JSON：`ran subagent_send {"subagent_type":"general-purpose","task":"<整段 prompt>"…}`（状态库里截图那次调用的 `summary` 原文） | `ToolCallCard.vue` 的标题取 `step.summary`；`summary` 来自 012 的 A4 |
| W2 | 展开后的正文是 `output:` + JSON 代码块，JSON 里套着转义的 JSON | 正文取 `display_body`，即 012 的 A3 |
| W3 | `subagent_run` 卡片展开后是 `subagent type: X` + **整段派发 prompt**；`subagent_fanout` 是 `N tasks (type):` + 每个任务的 prompt 片段（按字节截断加 `...`） | 012 的 A6；prompt 本应只是 subagent 视图的第一条消息 |
| W4 | `subagent_run`/`subagent_fanout` 没有 TUI 那样的实时任务卡片：没有每个任务的状态、工具次数、最近一次工具、失败原因，任务行也不能点进 subagent 视图 | 网页对这两个工具只用通用 `ToolCallCard`；任务状态散落在消息底部的生命周期卡片里 |
| W5 | 每个 subagent 在**消息末尾**各有一张"已派发"和一张"已结束"卡片（fanout 3 个任务就是 6 张），与它们的派发调用分离，位置也不在时间线上调用发生的地方 | `appendSubagentCard` 把卡片挂在 `ChatMessage.subagentCards`，`ChatView.vue:226` 在消息正文之后统一画出 |
| W6 | 生命周期卡片的细节行是 `[subagent-6e5c042d-…] 计划001…` 与 `status=ok`：原始 task id、键值对原文 | `SubagentCard.vue` 的 `detail` 直接拼 `[${taskId}]`、`status=${status}` |
| W7 | 嵌套派发（subagent 自己调 subagent_*）时，孙 agent 的生命周期卡片画进**主对话**（为它新建一条空的 assistant 消息 `assistant-run-events-<runId>`） | `eventProjection` 按 `parentRunId` 找宿主消息，找不到就在主对话里新建；没有把"父运行是某个 subagent"识别出来 |
| W8 | 截图里的 `subagent_send` 派发的 subagent 与它的调用卡片对不上 | 012 的 A1（事件里没有 `parent_tool_call_id`） |
| W9 | 重新载入历史后，`subagent_*` 卡片显示落库的旧正文（JSON 或 prompt） | `toolStepFromRow` 只读落库的 `tool_display` 部分 |
| W10 | 任务行看不出跑了多久；owner 要求与 TUI 相同：运行中的任务名后面有走动的计时，结束的（成功、失败、取消等）有最终耗时；不要单独的工具次数统计 | 网页没有任务行；`SubagentLifecycleCard` 不带开始/结束时间 |
| W11 | 重载一个 gateway 中途退出过的会话时，没跑完的 subagent 永远显示运行中（加了计时会一直走） | 与 TUI 的 T23 同一根因，由 SRT-004 的回收器发 `subagent_ended` 修；网页只需按 ended 收尾并用载荷里的 `finishedAtMs` 算耗时 |

## 设计（owner 确认的样式以 `subagent-cards-preview.html` 的"网页"页为准）

### 1. 一张卡片组件

新建 `frontend/src/components/chat/SubagentCallCard.vue`，替换两种旧呈现：时间线里 `subagent_*` 工具的 `ToolCallCard`（W1–W4），以及消息末尾的 `SubagentCard`（W5、W6）。删除 `SubagentCard.vue`、`ChatMessage.subagentCards`、`appendSubagentCard`、`SubagentLifecycleCard` 类型，以及 `ChatView.vue:226-234` 那段渲染。

卡片结构（与 TUI 同一份信息，网页自己的样式）：

- **头部**：一句话（第 5 节的文案键），右侧是时长（何时写见第 2 节最后一条）；进行中显示与其它运行中工具卡片相同的进行中标记。调用失败时头部下面一行是错误原文（`--forebrain-danger`）。
- **任务行**：每个任务一行，可点击（整行是按钮，`@click` → `emit('open', agentId)`，ChatView 接到 `openAgentView`）；没有 agent 的行（没开始的、被跳过的）不可点。行内：状态图标（`lucide-vue-next`：完成 `CheckCircle2`/`--forebrain-success`，失败 `XCircle`/`--forebrain-danger`，取消 `Ban`/`--forebrain-muted-text`，跳过 `AlertTriangle`/`--forebrain-warning`，运行中用与 `ToolCallCard` 运行中相同的转圈标记）+ 标题（单行，CSS 截断，`title` 属性是完整标题）+ **紧跟在标题后的时长**（W10，`--forebrain-muted-text`、`font-variant-numeric: tabular-nums`，不随标题截断而被挤掉）：运行中是每秒走一次的计时，用现有的 `formatRuntimeDuration`（`useChatStream.ts:1380`，与 TUI 的 `formatWorkingElapsed` 逐字相同）；已结束（成功、失败、取消）是最终耗时，用新加的 `formatToolDuration`（与 TUI 的 `formatDuration`，`pkg/tui/render.go:2354`，逐字相同：`<0.1s`、`4.2s`、`14m3s`）；没开始过的（等待中、跳过）不写。第二行是灰色说明，没有就不画：派发类为 `最近：<工具标签>`，后面再有工具调用时加 ` · 另 N 次工具调用`（与 TUI 第三层 `└ <标签>`、`… +N tool uses` 是同一组事实）；**不显示单独的工具次数统计**（与 TUI 的 T21 一致）。查询类为第 5 节的状态短语（列表的类型、等待超时、已请求停止）。失败/跳过的原因另起一行。
- 只用纯色，不用渐变；颜色全部取 `frontend/src/assets/main.css` 已有的 `--forebrain-*` 变量，不新增颜色。
- 空列表：显示网页工具卡片统一的空态文案（先 `grep -rni "no output\|无输出" frontend/src ../ai-elements-vue/packages/elements/src` 找到它并复用；确实没有就加 `chat.toolNoOutput`，英文 `(no output)`、中文 `（无输出）`，并在报告里说明）。

### 2. 卡片的数据

- `SubagentToolStep` 加 `subagentCall?: SubagentCall`（TS 类型与 012 的 Go 结构一一对应，字段用 camelCase）。实时：`toolStepFromPayload` 从 `payload.toolMeta.subagentCall` 取；历史：`toolStepFromRow` 从行上的 `subagentCall`（第 4 节，网关新增）取，**不再**读落库的显示正文（W9）。
- **绑定**（TS 中对应 013 的 `cardTaskOfAgent`）：`useChatStream` 维护 `subagentCallTasks: Map<string /* stepId */, Map<number /* index */, CallTaskLive>>` 和 `currentCallTaskOfAgent: Map<string /* agentId */, {stepId, index}>`。`CallTaskLive = {agentId, executionId, status, error?, startedAt?, finishedAt?, toolCount, latestTool?}`。
  - `subagent_spawned`：`parentToolCallId` 命中某个已知调用卡片的 `stepId`（主对话或任一 subagent 的时间线里）→ 绑定第 `taskIndex` 个任务；否则建一个"无派发调用"的时间线块（第 3 条）。
  - `subagent_ended`：先按 `parentToolCallId+taskIndex`，再按 `currentCallTaskOfAgent`；写入结果。
  - subagent 的工具事件：`currentCallTaskOfAgent[agentId]` 对应的任务 `toolCount` 按不同的 `stepId` 计数（只数开始事件，与 TUI 的 `appendFanoutTaskTool` 一致），`latestTool` 取 `toolMeta.invocation`。
  - 查询类调用还在进行时，行标题取 `currentCallTaskOfAgent` 已知的该 agent 的标题、不写状态短语；不知道就先不画行（与 013 第三节相同）。
  - 卡片的视图模型由一个**纯函数** `subagentCallView(step, live, now)` 生成（头部文案键与参数、每行的图标/标题/说明/agentId），放在 `useChatStream.ts` 里导出，单测直接测它。没有被生命周期绑定的任务用 `step.subagentCall` 的状态；查询类卡片整张按事实。
- 任务计时：开始 = 绑定它的 `subagent_spawned` 事件的 `createdAt`（查询类用事实的 `startedAt`）；结束 = `subagent_ended` 载荷的 `finishedAtMs`（012 新增；旧事件没有就用该事件的 `createdAt`；查询类在结果已结束时用事实的 `finishedAt`）。查询行的事实 `executionId` 等于该 agent 当前跟踪的执行、之后该执行结束时，查询行也改为结束图标和最终耗时（与 TUI 第九节相同）。走动的计时用一个每秒更新的 `now`（`useChatStream` 里只在"当前可见的卡片里有运行中的任务"时启动，没有时停掉，不留空转的定时器），视图模型函数 `subagentCallView(step, live, now)` 以它为参数。
- 卡片头时长：与 TUI 第二节相同——只有一个任务的派发类卡片不写（行上已有），多任务 fanout 写整批调用时长，查询类写调用时长（工具的 `durationSeconds`，用 `formatToolDuration`）。

### 3. 时间线位置

- 有派发调用的：卡片就在该调用的 `tool` 块原位（`ChatView.vue` 与 `SubagentConversation.vue` 里 `block.kind === 'tool' && block.step.subagentCall` 时画 `SubagentCallCard`，否则照旧 `ToolCallCard`）。嵌套派发的卡片因此自然在 subagent 视图里（W7 的一半）。
- plan review（D13，与 TUI 第二节相同，不加主语）：收到 `plan_review_started`（计划 015）时在宿主时间线里开一块卡片，`stepId = reviewId`，一个任务（`Plan review`、`plan-reviewer`、等待中），头部 `subagentCard.review.starting`；reviewer 的 `subagent_spawned` 以 `parentToolCallId == reviewId`、`taskIndex == 0` 绑定，头部变 `review.running`，结束 `review.done`；收到 `Outcome == 'failed'` 的 `plan_reviewed` 而任务从未开始，头部 `review.failed`，下面一行是 `error` 原文。其它没有派发调用的执行用 `run.running`/`run.done`。
- 没有派发调用的执行（plan-reviewer 等，D11）：`TimelineBlock` 新增 `{ kind: 'subagent'; id: string /* 'subagent-exec:' + executionId */; stepId: string }`，实时追加到宿主时间线末尾，重载时同样追加到宿主时间线末尾（宿主找法见下）。同一 `executionId` 幂等。
- 宿主：`parentRunId` 是某个 subagent 的执行 id（`subagents` 里某条记录的当前或历史 `executionId`）时，宿主是那个 subagent 的时间线，**不得**在主对话里新建空消息（W7 的另一半）；否则沿用现有 `eventProjection` 的找法。
- 旧的"遗留账本"路径（`legacySubagents`，事件日志之前的会话）：每条记录建一个"无派发调用"块，代替原来的 `appendSubagentCard`。

### 4. 网关：历史行带上事实

`pkg/gateway/api_extra.go` `handleChatMessages`（`:1155`）：在构造 `msg` 前算一次 `subagentCalls := turn.SubagentCallsInTranscript(turns)`（012），`msg` 加字段 `SubagentCall *event.SubagentCall \`json:"subagent_call,omitempty"\``，工具行按 `t.ToolStepID` 取。只做这一处传输层改动；推导逻辑全部在 012。`frontend/src/lib/api.ts` 的 `ChatMessageRecord` 加 `subagentCall?`。

### 5. 文案（`frontend/src/locales/index.ts`，中英文同键）

头部（`{count}`、`{type}`；英文需要单复数时用 `…One`/`…Other` 两个键，中文两键同文；按现有 `t()` 的插值写法，不引入新库）：

| 键（示意） | 英文 | 中文 |
| --- | --- | --- |
| `subagentCard.run.running` | Running {count} {type} task(s)… | 正在运行 {count} 个 {type} 任务… |
| `subagentCard.run.done` | Ran {count} {type} task(s) | 已运行 {count} 个 {type} 任务 |
| `subagentCard.run.failed` | Failed to run {count} {type} task(s) | 未能运行 {count} 个 {type} 任务 |
| `subagentCard.send.starting` | Starting {count} {type} task(s) in background… | 正在后台启动 {count} 个 {type} 任务… |
| `subagentCard.send.running` | Running {count} {type} task(s) in background… | 正在后台运行 {count} 个 {type} 任务… |
| `subagentCard.send.done` | Ran {count} {type} task(s) in background | 已在后台运行 {count} 个 {type} 任务 |
| `subagentCard.send.failed` | Failed to start {count} {type} task(s) in background | 未能在后台启动 {count} 个 {type} 任务 |
| `subagentCard.continue.*` | Continuing / Continued / Failed to continue … | 正在继续 / 已继续 / 未能继续 … |
| `subagentCard.status.*` | Checking / Checked / Failed to check … | 正在查看 / 已查看 / 未能查看 … |
| `subagentCard.wait.*` | Waiting for / Waited for / Failed to wait for … | 正在等待 / 已等待 / 未能等待 … |
| `subagentCard.close.*` | Stopping / Stopped / Failed to stop … | 正在停止 / 已停止 / 未能停止 … |
| `subagentCard.list.*` | Listing tasks… / Listed {count} task(s) / Failed to list tasks | 正在列出任务… / 已列出 {count} 个任务 / 未能列出任务 |
| `subagentCard.review.starting` / `running` / `done` / `failed` | Starting {count} {type} task(s)… / Running {count} {type} task(s)… / Ran {count} {type} task(s) / Failed to start {count} {type} task(s) | 正在启动 {count} 个 {type} 任务… / 正在运行 {count} 个 {type} 任务… / 已运行 {count} 个 {type} 任务 / 未能启动 {count} 个 {type} 任务 |
| `subagentCard.canceled` | Canceled | 已取消 |
| `subagentCard.breakdown` | {done} done, {failed} failed, {cancelled} cancelled, {skipped} skipped（只列非零项） | {done} 个完成、{failed} 个失败、{cancelled} 个取消、{skipped} 个跳过 |
| `subagentCard.latestTool` | latest: {label} | 最近：{label} |
| `subagentCard.moreToolUses` | +{count} tool use(s) | 另 {count} 次工具调用 |
| `subagentCard.phrase.waitEnded` / `stopRequested` | still running when the wait ended / stop requested | 等待结束时仍在运行 / 已请求停止 |
| `subagentCard.emptyTask` | (empty task) | （空任务） |

键名以现有 `chat.*` 命名风格为准，可以调整，但中英文必须同键；删除不再使用的 `chat.subagentSpawned`、`chat.subagentEnded`、`chat.subagentOpen`。类型名（`general-purpose` 等）不翻译。工具标签（`latestTool` 里的 `{label}`）是 012 的英文调用标签，与 TUI 第三层相同，不翻译。

## 现状摘录（`bda9505`）

- `frontend/src/views/ChatView.vue:182-187` 时间线里的工具块：`<ToolCallCard v-else-if="block.kind === 'tool'" class="my-2" :step="block.step" :default-open="false" />`；`:226-234` 消息末尾 `v-if="msg.role === 'assistant' && msg.subagentCards?.length"` 画 `SubagentCard`。
- `frontend/src/components/chat/SubagentConversation.vue:56` subagent 视图里的工具块同样是 `ToolCallCard`。
- `frontend/src/composables/useChatStream.ts`：`toolStepFromPayload`（约 `:366`）、`toolStepFromRow`（约 `:393`）、`subagentStepSummary`（约 `:953`）、`appendSubagentCard`（约 `:1956`）、`subagent_spawned`/`subagent_ended` 处理（约 `:2421`、`:2457`）、`eventProjection`（约 `:1558`）、遗留账本路径（约 `:3407`）。
- `frontend/src/components/chat/SubagentCard.vue` 的 `detail`：`[${taskId}]`、`status=${status}`。
- 网页事件载荷与历史行都经过 `toCamelCase`（`frontend/src/lib/forebrainGatewayRuntime.ts:428`、`frontend/src/lib/api.ts:1100`）；`toolMetaJson` 是保持 snake_case 的 JSON 字符串，读它用现有的 `metaString` 写法（同时认 snake 和 camel）。

## 缓存影响

无。网页呈现与网关的历史接口都不进入模型请求；网关只多返回一个字段。

## 范围

**只改这些文件：** `frontend/src/components/chat/SubagentCallCard.vue`（新建）、`frontend/src/components/chat/SubagentCallCard.test.ts`（新建）、`frontend/src/components/chat/SubagentCard.vue`（删除）、`frontend/src/components/chat/SubagentConversation.vue`、`frontend/src/views/ChatView.vue`、`frontend/src/composables/useChatStream.ts`、`frontend/src/composables/useChatStream.test.ts`、`frontend/src/locales/index.ts`、`frontend/src/lib/api.ts`、`pkg/gateway/api_extra.go`、`pkg/gateway/api_extra_test.go`；网页 e2e 的用例文件（`frontend/e2e/` 下，照现有 spec 新建 `subagent-cards.spec.ts`）。

**不要动：** `pkg/gateway/dist`（不要运行 `make ui` 或 `pnpm build`）；`ToolCallCard.vue` 对普通工具的行为；`AgentViewTabs.vue`（010）；subagent 视图里的对话输入（008）；任何 Go 推导逻辑（012）。

## 步骤

### 第 1 步：先写失败的测试

`frontend/src/composables/useChatStream.test.ts` 新增（照该文件已有的"喂事件、看 messages/subagents"用例的写法）：

0. `formatToolDuration` 的表驱动测试：用 `pkg/tui/render_test.go` 里 `formatDuration` 的测试用例（执行者用 `grep -n "formatDuration" pkg/tui/*_test.go` 找到）逐条照抄输入与期望，结果必须逐字相同；没有现成用例就按 `pkg/tui/render.go:2354` 的分支各取一个值（0、50ms、4.2s、59s、14m3s 对应的毫秒数）。
1. 喂截图那次的事件序列（`tool_call_started`/`tool_call_completed` 的 `subagent_send`，`toolMeta.subagentCall` 用 012 推导出的形状手写；`subagent_spawned`/`subagent_ended` 带 `parentToolCallId`）：断言主对话里该工具块的 `subagentCallView` 头部键是 `send.done`、头部没有时长、行标题 `计划001 Go车道实施`、行时长是 `formatToolDuration(finishedAtMs - spawned.createdAt)`、行可点的 `agentId` 是 `subagent-6e5c…`、行里没有任何"N 次工具调用"的独立统计；把 `now` 设到 ended 之前再算一次，行时长是 `formatRuntimeDuration(now - spawned.createdAt)`；断言消息上**没有** `subagentCards` 字段、时间线里没有额外的 `subagent` 块。
2. 查询类（status/wait 超时/close/list 空与非空/continue）各一个用例，断言视图模型的头部键和行说明键。
3. fanout 两个任务、一个跳过：视图模型显示一个完成、一个跳过，头部带 breakdown。
4. `parentToolCallId` 为空的 plan-reviewer：时间线里出现一个 `kind: 'subagent'` 块，主对话里不新建空消息。
5. 嵌套：父运行是某个 subagent 执行的 `subagent_spawned`，断言卡片/块落在那个 subagent 的 `blocks` 里，`messages` 数量不变。
6. 历史：`conversationFromTranscript` 喂一条带 `subagentCall` 的工具行（`partsJson` 的 `tool_display` 正文故意是旧 JSON），断言工具块的 `step.subagentCall` 存在、`step.output` 不被卡片使用。

`frontend/src/components/chat/SubagentCallCard.test.ts`（照 `LspRecommendationCard.test.ts`）：渲染各状态，断言文字里没有 `{`、没有 `subagent-` 开头的原始 id、没有 `status=`、没有 `tool uses`/`次工具调用` 结尾的独立统计；运行中的行用假定时器（`vi.useFakeTimers()`）推进 3 秒后计时文字前进 3 秒；点击可点行发出 `open` 事件带 agentId，不可点行不发；中英文切换后头部文字跟着变。

`pkg/gateway/api_extra_test.go`：照该文件里 `handleChatMessages` 的已有测试，新增一条：transcript 含一次 `subagent_status` 调用与结果行，断言响应里该行有 `subagent_call`，内容与 `turn.SubagentCallsInTranscript` 一致。

**验证**：`cd frontend && corepack pnpm test -- useChatStream SubagentCallCard` → 新用例失败（组件文件不存在也算）；`CGO_ENABLED=1 go test -tags fts5 ./pkg/gateway -run '<新测试名>' -count=1` → 失败。

### 第 2 步：网关字段（设计第 4 节）

**验证**：`CGO_ENABLED=1 go test -tags fts5 ./pkg/gateway -count=1` → `ok`。

### 第 3 步：数据与绑定（设计第 2、3 节）

**验证**：第 1 步 `useChatStream.test.ts` 的 6 条转绿；`grep -n "subagentCards\|appendSubagentCard\|SubagentLifecycleCard" frontend/src -r` → 无输出。

### 第 4 步：组件与文案（设计第 1、5 节）

**验证**：`cd frontend && corepack pnpm test` → 全部通过；`cd frontend && corepack pnpm exec vue-tsc --noEmit -p tsconfig.json` → 退出码 0；`ls frontend/src/components/chat/SubagentCard.vue` → 不存在。

### 第 5 步：网页真机

1. 假模型：在 `frontend/e2e/subagent-cards.spec.ts` 里按现有 spec 的方式启动（`scripts/acceptance/web_e2e.sh` 会构建到临时目录并跑全部 spec），脚本化一次 `subagent_fanout`（含一个空 prompt 任务）和一次 `subagent_list`；断言页面上卡片头、任务行、跳过原因、运行中的任务名后有计时且 3 秒后前进、结束的任务名后有最终耗时、没有工具次数统计行、点击任务行进入 subagent 视图、页面文字不含 `{"` 和 `status=`。
2. 真实模型：`FOREBRAIN_E2E_REAL_LLM=1 scripts/acceptance/web_e2e.sh`，用与 013 第 7 步相同的提示（send → status → list → wait → close）；断言同上（文字断言放宽为"出现五张 subagent 卡片、都不含 JSON"）。截图存证。
3. 重新载入页面，截图与实时一致。

**验证**：两次运行最后一行都是 `web e2e: PASS`；截图附在报告里。

## 测试计划

`useChatStream.test.ts` 6 条、`SubagentCallCard.test.ts` 若干、`api_extra_test.go` 1 条、e2e spec 1 个。

## 完成标准

- [ ] 第 1–5 步的验证全部通过
- [ ] `cd frontend && corepack pnpm test`、`vue-tsc` 通过；`CGO_ENABLED=1 go test -tags fts5 ./... -count=1` 全部 `ok`
- [ ] `git status pkg/gateway/dist` 无改动
- [ ] 中英文文案键一一对应（`grep -c "subagentCard\." frontend/src/locales/index.ts` 是偶数，报告里列出）
- [ ] 截图覆盖 W1–W10（W11 的真机检查在计划 009 里做，它依赖 SRT-004）
- [ ] README 状态行已更新

## STOP 条件

- 前置检查不满足（尤其预览未经 owner 确认）。
- 需要改 Go 推导逻辑（`pkg/tool`、`pkg/turn`）才能拿到某个事实：回到 012。
- 发现网页的卡片说的和 TUI 不一致而 TUI 的说法有问题：回到 013，不要在网页上自定一套。
- e2e 需要运行 `make ui`/`pnpm build` 才能进行。

## 维护说明

- 头部文案与 TUI 第二节的表是同一张表的两种语言；改一处必须改另一处，两边的测试各有一条对照。
- 卡片的绑定规则与 TUI 相同，只认 `parentToolCallId+taskIndex` 与"agent 当前执行属于哪张卡片"；不要加按顺序猜的回退。
- 计划 008 的 subagent 对话输入落地后，用户发起的执行（`origin='user'`）没有派发调用，会得到一个"无派发调用"块；它在主对话里是否出现，与 TUI 一样由计划 007 定，008 照做。
