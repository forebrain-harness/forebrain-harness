# 计划 013：TUI——八个 subagent_* 工具都画成与 fanout 同一种卡片

> **执行者须知**：逐步执行。每一步都运行"验证"命令并确认结果后再进入下一步。出现"STOP 条件"里的任何情况，立即停止并报告。
> 完成后更新 `docs/plan/SUBAGENT_CONVERSATION/README.md` 里本计划的状态行。**不要提交代码。** 先读 README 的"全局规则"和"subagent_* 工具卡片审计"一节。
>
> **前置检查**：README 里计划 001、010、012、015 必须是 `DONE`（001 给 driver.sh 加的 `click` 命令是第 7 步真机要用的）；决策 D10、D11 已定夺；`docs/plan/SUBAGENT_CONVERSATION/subagent-cards-preview.html` 里的终端样式已经 owner 确认（README 决策表 D12 的结论不是"待定"）。任何一条不满足就 STOP。
>
> **漂移检查（先运行）**：
> `git diff --stat bda9505 -- pkg/tui/reducer.go pkg/tui/render.go pkg/tui/notify.go pkg/tui/commands.go pkg/tui/chat_turn.go pkg/tui/chat_surface.go pkg/tui/tty_session.go`
> 计划 001、010、015 会改其中若干文件（roster 光标、roster 行、`SubagentTaskTitle`、015 的评审事件与模型字段），那是预期的。其余改动按函数名和注释原文核对"现状"摘录，对不上就 STOP。

## 状态

- **优先级**：P1
- **工作量**：L
- **风险**：MED（`pkg/tui/reducer.go` 的 subagent 卡片状态机整体换成精确绑定；回放路径一起改）
- **依赖**：001（第 7 步真机用它的 `$D click`）、010、012、015（plan review 卡片用 015 发布的 `plan_review_started`/`plan_reviewed`；T23 的收尾由 SRT-004 的回收器提供，见 T23；本计划不等它，009 等它）
- **类别**：bug
- **基线**：提交 `bda9505`，2026-10-05

## owner 的要求（逐字）

> 如 forebrain harness tui截图所示，这两个subagent_*工具的消息卡片不符合需求，output区域是原始json数据，必须定位根因并修复bug，对齐subagent_fanout的消息卡片的UI/UX，重新设计，必须在docs/plan/SUBAGENT_CONVERSATION目录下生成新计划文件

> 必须全面审计所有的subagent_*工具的消息卡片的UI和UX，找出所有问题，定位根因并设计修复计划，加入本期计划中

> （2026-10-05，看过预览后）把 · 66 tool uses 这行统计删掉

> （2026-10-05，plan-reviewer 缺陷报告的第 4 点，见计划 015）exit_plan_mode工具审批后，显示subagent plan-reviewer spawned plan-reviewer消息文案需要重新设计，这个文案语义有错误，应该是主agent spawned plan-reviewer subagent，必须改成语义正确且更容易理解的文案

> （2026-10-05，D13）没有派发调用的执行 plan-reviewer的消息卡片的header里不要加主语，与其他的subagent_*工具调用消息卡片的header保持一致使用Starting/Running/Ran/Failed to start等

> （2026-10-05）✓ 任务16 migrate 导入、✗ 任务17 扩展目录和计划002 Web车道实施这样的任务名称后面加上运行时长计时和最终耗时
>
> 运行中的加上运行时长计时，执行结束的（包括成功、失败、取消等）的加上最终耗时

## 审计发现（TUI，逐条，含根因）

共同根因 A1–A8 见计划 012。下面是 TUI 自己的问题，编号 T1–T23；每条都在本计划里修。T21、T22 是 owner 看过预览后追加的要求，T23 是为 T22 核对代码时顺带发现的既有缺陷（根因修复在 SRT-004，本计划只消费它的结果）。

| # | 现象 | 根因（代码位置以 `bda9505` 为准） |
| --- | --- | --- |
| T1 | `subagent_send/status/wait/continue/close/list` 的卡片 output 区是 `{"output":"{\"agent_id\"…"}` 代码块（两张截图） | 012 的 A3；TUI 只把 `subagent_fanout`、`subagent_run` 送进卡片路径（`pkg/tui/reducer.go` `reduceMessage` 里 `if toolName == "subagent_fanout" \|\| toolName == "subagent_run"`），其余六个当普通工具卡片画 `display_body` |
| T2 | `subagent_send` 卡片头 `Sent agent 计划001 Go车道实施 general-purpose · <0.1s`：报的是派发调用的 20ms，subagent 实际跑了十几分钟；卡片下面另起一行 `subagent general-purpose spawned general-purpose [subagent-6e5c…]`，结束时再来一行 `… ended … status=ok`——类型说两遍、露出原始 task id、没有任务名、`status=ok` 是键值对原文；与 fanout 卡片完全两样 | send 不走卡片路径；它的 subagent 因 012 的 A1 找不到卡片，落到 `SubagentSpawnedMsg` 处理末尾的生命周期文字（`reducer.go` `title := "subagent spawned"`、`subagentSummary`） |
| T3 | `subagent_run`/`subagent_send` 头部分支的 target 先取 `title`，为空回退读输入里的 `description` 字段并 `truncateForDisplay(d, 60)`；send 的后缀在没有 `subagent_type` 时回退到 `meta.AgentType` | 任何 subagent 工具的参数里都没有 `description`（`pkg/run/subagent.go:85-95`），回退是死分支，且违反"头部不截断"；`meta.AgentType` 是 `EnrichToolMeta` 从上下文取的**调用方**类型，不是被派发的类型（`pkg/tool/format.go` `EnrichToolMeta`），嵌套派发 fork 时会写错 |
| T4 | `status/wait/continue/close` 卡片头是原始 `subagent-6e5c042d-…`（截图二）；按 `run_id` 或不给 id（"最近一个"）查时头部什么都不写 | `pkg/tui/render.go` 这几个分支只读输入 `task_id`（`:2905-2951` 一带） |
| T5 | `subagent_wait` 超时返回、subagent 仍在跑时，卡片头写 `Agent done` | 同上分支只按调用状态取词，不看结果里的 `timed_out` |
| T6 | `subagent_continue`：原来那张派发卡片（例如 fanout）被"复活"成运行中；continue 自己的卡片是带最多 24KB 输出的 JSON；若原派发是 send，再冒出一对新的 spawned/ended 文字行 | `SubagentSpawnedMsg` 先按 `ParentToolCallID+TaskIndex` 找不到（012 的 A2），再 `findFanoutByAgentID` 找到旧卡片并改回 running；找不到就走生命周期文字 |
| T7 | `subagent_list` 卡片是所有记录的 JSON，含每条的整段 prompt 和输出 | T1 |
| T8 | `subagent_close` 卡片是 `{"status":"cancel_requested","record":{…}}` | T1 |
| T9 | `subagent_run` 卡片头 `Running 1 general-purpose tasks…` / `Ran 1 tasks` | `renderFanoutSummary` 固定写 `tasks`；`pkg/tui/reducer_test.go:587`、`:1143` 甚至把错的复数断言成了期望值 |
| T10 | fanout 仍在派发时，任何一个对不上号的 spawned（`subagent_send` 派发的、plan-reviewer）会被挂到 fanout 的某个等待行上：那一行标题被别的任务顶替，点击打开错的 agent，真正的任务永远绑不上 | `findFanoutWithWaitingTask` + `assignFanoutTaskAgent`：按"第一个等待的任务"猜，而 `fanoutStates` 是 map，"第一个"还是随机的 |
| T11 | 空 prompt 被跳过的任务、`fail_fast` 跳过的任务在实时卡片上显示 `✓` | 012 的 A5：`applyFanoutResults` 解析的是显示文字，永远失败；`settleUnreportedFanoutTasks` 把没收到结束事件的任务一律标 `done` |
| T12 | fanout/run 调用本身失败（`subagent capacity is 0`、类型策略拒绝、`fail_fast` 返回错误）时，卡片只把任务全标 `✗`，**看不到为什么** | 卡片没有显示调用错误的位置；`msg.Content` 里的 `**error** (…)` 被 `applyFanoutResults` 当 JSON 解析后丢弃 |
| T13 | 有任务被取消时卡片头仍是 `Ran 3 tasks` | `renderFanoutSummary` 只统计 done/failed/skipped |
| T14 | fanout 卡片正文（任务名、统计、错误、工具进度）是半亮度 | `pkg/tui/render.go` `renderFanout` 里 `lipgloss.NewStyle().Faint(true)`，违反"TUI 内容文本不得用 dim/faint"的规矩 |
| T15 | 死代码：`ActivityStatusUpdatedMsg` 全仓库没有任何生产者（`grep -rn ActivityStatusUpdated --include=*.go .` 只有定义和处理），卡片的 `⎿ activity` 行、`FanoutTaskState.Activity`、`updateFanoutTaskActivity` 跟着全死；`FanoutTaskState.TokenCount` 只写不读，却让**每一个** usage delta 都重发一次卡片帧（`reducer.go` `TokenUsageDeltaMsg` 分支里 `updateFanoutTaskTokens` 后 `emitFanoutFrame`） | 历史遗留 |
| T16 | general-purpose subagent 自己调用 `subagent_run`/`subagent_fanout`（类型策略允许：`pkg/tool/permissions.go` 的 `typedSubagentDisallowedTools` 里没有 general-purpose）时，这张卡片画进了**主视图**；它派发的孙 agent 的生命周期文字也进主视图 | `emitFanoutFrame` 构造的帧没有 `AgentID`，渲染器按"没有 agent 就属于主视图"路由（`reducer.go` 里 `if f.AgentID != "" && !f.SubagentLifecycleCard`）；违反"subagent 的一切属于它自己的视图" |
| T17 | 空 prompt 的 `subagent_run` 画成普通工具卡片，头部是成功的 `Agent`，正文是 `{"status":"skipped",…}`；空 prompt 的 `subagent_send` 同样显示成功 `Sent agent` + JSON | `parseFanoutTasksFromMeta` 在没有 `task` 时返回 nil，`handleFanoutToolMessage` 退回 `buildFrame(FrameTool, …)` |
| T18 | plan-reviewer 等由运行时派发、没有派发调用的 subagent，只有灰色（244）的 `subagent plan-reviewer spawned plan-reviewer [plan-review-…]` 文字行，与 fanout 卡片两样，任务名（`Plan review`）也看不到 | 同 T2 的生命周期文字；`renderStatus` 用 244 灰色画整行 |
| T19 | 修好之后旧会话回放仍是 JSON：`subagent_*` 工具行落库的 `tool_display` 正文就是当时的 JSON | `replayToolMessage`（`pkg/tui/commands.go:981`）优先用落库的显示正文 |
| T20 | 把查询类工具改成卡片后，回合被中断时它们会永远停在"进行中"；回放时没有结果行的查询调用不显示 | `Renderer.finalizePendingToolsVM`（`pkg/tui/render.go:757`）只处理 `FrameTool`；`orphanToolCalls`（`commands.go`）只排除 run/fanout |
| T21 | 已结束的任务下面有一行 `· 66 tool uses` 统计，owner 要求删掉 | `renderFanoutContent`（`pkg/tui/reducer.go:1739-1746` 一带）在 done/failed/skipped 时拼 `fmt.Sprintf("%d tool uses", t.ToolTotal)` 并以 `  · ` 前缀输出；全仓库只有这一处产出这行（`grep -rn 'tool uses' pkg frontend/src` 核实；`:1761` 的 `… +N tool uses` 是第三层溢出行，保留） |
| T22 | 任务行只有名字，看不出跑了多久；owner 要求运行中的任务名后面有运行时长计时，结束的（成功、失败、取消等）有最终耗时 | 卡片任务状态 `FanoutTaskState` 没有开始/结束时间；卡片内容由 reducer 预先拼成字符串（`renderFanoutContent`），渲染时不会再变，`renderBlockFull`（`pkg/tui/reducer.go:2458`）也只把运行中的 `FrameTool`、未完成的压缩和技能安装当作需要随时间重画的块 |
| T23 | 恢复（`/resume`）一个上次没正常结束的会话时，上个进程里没跑完的 subagent 没有 `subagent_ended` 事件，它的卡片任务永远停在运行中（fanout 卡片头永远是 `Running …`）；加上 T22 的计时后会一直走下去，显示成跑了几天 | 根因不在界面：进程退出后没有任何代码给它没跑完的运行收尾（"每个 spawned 终将有一个 ended"不成立）。这已由 `docs/plan/SCHEDULED_RUNS_TRANSCRIPT/004-run-liveness-and-session-exclusivity.md`（SRT-004，D8）修：进程租约过期后，回收器把被放弃的子运行标为 failed 并发 `subagent_ended`（`Error` 为 `AbandonedRunReason`）。本计划不在界面上另做"这个执行还活不活"的判断（那会是进程内的猜测，gateway 多副本时也不成立），只要求 ended 事件带上真实的结束时间（012 的 `FinishedAtMs`，SRT-004 已按本计划补一句），卡片据此收尾 |

## 设计（owner 确认的样式以 `subagent-cards-preview.html` 的"终端"页为准）

### 一、一种卡片，一套规则

八个 `subagent_*` 工具、以及没有派发调用的 subagent 执行（plan-reviewer 等，D11），全部画成现在 fanout 用的那种卡片（`FrameFanout`，由 `renderFanout` 绘制）：一行头部，下面每个任务一行（`└ ` 开头，状态图标 ✓ ✗ × !，运行中无图标），任务下面是失败/跳过的原因、最近一次工具调用和 `… +N tool uses`（**没有** `· N tool uses` 统计行，T21）。**不再有** `subagent X spawned` / `subagent X ended` 文字行（D11）。卡片里每一行任务都可点击，进入那个 subagent 的视图。名字沿用 `FrameFanout`、`fanoutState`、`renderFanout`（不改名，减少差异面），但把它们的注释改成"subagent 卡片"。

### 二、卡片头（英文，终端唯一的写法；网页按 014 用同义的中英文）

`<N> <类型> task(s)`：全部任务类型相同且已知时带类型；N 为 1 时用 `task`。类型 fork 写 `fork`。

| 工具 | 进行中（`○`） | 结束（`●`） | 调用失败（`●`，下一行是错误原文） |
| --- | --- | --- | --- |
| `subagent_run` / `subagent_fanout` | `Running N <t> task(s)…` | `Ran N <t> task(s)` | `Failed to run N <t> task(s)` |
| `subagent_send` | subagent 尚未开始：`Starting N <t> task(s) in background…`；运行中：`Running N <t> task(s) in background…` | `Ran N <t> task(s) in background` | `Failed to start N <t> task(s) in background` |
| `subagent_continue` | `Continuing N <t> task(s)…` | `Continued N <t> task(s)` | `Failed to continue N <t> task(s)` |
| `subagent_status` | `Checking N task(s)…` | `Checked N <t> task(s)` | `Failed to check N task(s)` |
| `subagent_wait` | `Waiting for N <t> task(s)…` | `Waited for N <t> task(s)` | `Failed to wait for N task(s)` |
| `subagent_close` | `Stopping N task(s)…` | `Stopped N <t> task(s)` | `Failed to stop N task(s)` |
| `subagent_list` | `Listing tasks…` | `Listed N task(s)` | `Failed to list tasks` |
| plan review（审批浮层里"请另一个模型评审"，D13） | 已请求、reviewer 尚未开始：`Starting 1 plan-reviewer task…`；运行中：`Running 1 plan-reviewer task…` | `Ran 1 plan-reviewer task` | 还没开始就失败（模型未配置、没有计划等）：`Failed to start 1 plan-reviewer task` |
| 其它无派发调用的执行 | `Running 1 <t> task…` | `Ran 1 <t> task` | — |

- **plan review 的卡片与其它 `subagent_*` 卡片同一套头部，不加主语**（D13，owner 2026-10-05："没有派发调用的执行 plan-reviewer的消息卡片的header里不要加主语，与其他的subagent_*工具调用消息卡片的header保持一致使用Starting/Running/Ran/Failed to start等"）。为了让 `Starting` 和 `Failed to start` 有真实含义，评审请求本身就是这张卡片的"派发调用"：计划 015 在请求评审时发布 `plan_review_started`（带 `ReviewID`），并把 `ReviewID` 作为 reviewer 运行的工具调用 id，于是 reviewer 的 `subagent_spawned` 的 `ParentToolCallID == ReviewID`、`TaskIndex == 0`，按第四节的精确绑定接上这张卡片；评审结束时发布 `plan_reviewed`（`Outcome`、`Error`）。reducer 收到 `PlanReviewStartedMsg` 就以 `StepID = ReviewID`、一个任务（标题 `Plan review`、类型 `plan-reviewer`、`waiting`）开卡片，卡片的 `Verb` 记为 `review`（只在 TUI/网页的卡片状态里，不进 012 的事实）；`PlanReviewedMsg` 到来时若任务从未被 spawned 绑定、`Outcome` 为 `failed`，头部是 `Failed to start 1 plan-reviewer task`，头部下面一行是 `Error` 原文；`Outcome` 为 `stopped` 且未开始，按"调用被取消"一条处理，标签为 `review the plan with <provider/model>`。开始之后的失败、取消照派发类的规则：任务行 `✗`/`×` 加原因，头部 `Ran 1 plan-reviewer task · 0 done, 1 failed`。卡片出现后，TUI 不再另打 `notifyPlanReviewFailure` 那句"Plan review by … failed/was stopped"（卡片已经说了，说两遍是重复）；"The plan is still waiting for your decision" 由重新弹出的审批浮层本身表达。
- 调用被取消（`ToolMeta.Status` 为 `canceled`）：`Canceled ` + 012 的调用标签（与普通工具卡片"`Canceled` + 目标"的写法一致），例如 `Canceled run 3 general-purpose tasks`。
- 派发类（run/fanout/send/continue/无派发调用）结束时，若不是全部 done，追加 ` · 1 done, 1 failed, 1 cancelled, 1 skipped`（只列非零项；有非 done 项时 done 也列出）——在现有逻辑上补 cancelled（T13）。
- 卡片头的时长：只在它说的是任务行没说的事时才写。**只有一个任务的派发类卡片**（`subagent_run`、`subagent_send`、`subagent_continue`、plan review、无派发调用的执行，以及只有一个任务的 fanout）**头部不写时长**，因为那一行任务后面已经有它的耗时（T22），再写一遍是重复。多任务的 fanout 结束后头部写调用时长（整批的墙钟时间，`formatDuration`）；查询类卡片结束后头部写调用时长（`subagent_wait` 就是等了多久）。
- 颜色：头部沿用 214；正文**不再** `Faint`，与 assistant 正文同亮度（T14）。

### 三、任务行

派发类卡片（与现在 fanout 相同，只修 T11–T13，并删掉统计行 T21）：

```
  └ ✓ 计划001 Go车道实施 · 14m3s
      └ shell gofmt -l pkg cmd
        … +30 tool uses
  └ 计划002 Web车道实施 · 3m 12s          ← 运行中，每秒走一次
      └ edit frontend/src/views/ChatView.vue
        … +11 tool uses
```

- 标题：012 的事实里的 `Title`；为空（空 prompt 被跳过）写 `(empty task)`。
- **标题后面的时长（T22）**：` · <时长>`，与标题同一行、同亮度。运行中是从开始到现在、**随时间走**的计时，格式用 `formatWorkingElapsed`（`45s`、`3m 12s`、`1h 02m 05s`，与运行中的工具卡片和 working 行相同）；已结束（done、failed、cancelled，包括 SRT-004 回收器收尾的 failed）是最终耗时，格式用 `formatDuration`（`<0.1s`、`4.2s`、`14m3s`，与已结束的工具卡片相同）。从没开始过的任务（waiting、skipped）不写时长。时钟来源见第九节。
- **不画统计行**（T21）：删掉 `renderFanoutContent` 里拼 `"%d tool uses"` 的 `parts` 那段及其 `add("  · ", …)`；原因行（`t.Error`）保留。工具次数只以第三层的 `… +N tool uses` 出现。
- 原因：failed/skipped 的 `Error` 原文，缩进同现在。
- 第三层：最近一次工具调用（`└ <012 的调用标签>`）与 `… +N tool uses`，规则不变。

查询类卡片（status/wait/close/list）的任务行列出调用结果里的任务；图标和标题后的时长表示**这个任务的实际状态**（见第九节：结果说它在跑、之后本会话看到它结束，就变成结束的图标和最终耗时），状态短语一行只写调用当时才有意义的话：

```
  └ ✓ 计划001 Go车道实施 · 14m3s
  └ 计划002 Web车道实施 · 3m 12s
      · still running when the wait ended
```

- 状态短语行（没有就不画这一行）：`TimedOut` 时 `still running when the wait ended`；`StopRequested` 时 `stop requested`；failed 时下一行是原因原文。`running`、`done`、`cancelled` 不再写成短语——图标和时长已经说了。
- 调用还在进行（事实里只有输入的 `task_id`）时：任务行用 reducer 已知的该 key 的标题（`cardTaskOfAgent[key]` 指向的卡片任务的标题），不写状态短语；不知道标题（例如按 `run_id` 查、或这个 agent 不是本次 TUI 进程里派发的）就先不画行，调用结束后按事实补上。
- `subagent_list` 的每一行都有短语行，第一项是类型：`· explore`（有其它短语时 `· explore · stop requested`），因为列表里的任务类型可能各不相同。
- 列表为空：正文一行 `(no output)`（复用 `toolNoOutputText`）。

调用失败：错误原文作为头部下面的第一行（不属于任何任务，不可点击），任务行照常列出（没有自己结果的任务 `✗`）。

### 四、绑定只用精确标识（T6、T10）

`Reducer` 新增 `cardTaskOfAgent map[string]cardTaskRef`（`cardTaskRef{stepID string; index int}`），表示"这个 agent 当前这次执行属于哪张卡片的第几个任务"。

- `SubagentSpawnedMsg`：`findFanoutByStepID(m.ParentToolCallID)` 存在且有第 `m.TaskIndex` 个任务 → 绑定：任务 `Key`、`running`、开始时间；`cardTaskOfAgent[key]` 指向它。否则（没有派发调用；或 D10 选 B 时未迁移的旧 send 事件）→ 新建一张"无派发调用"的卡片，`StepID = "subagent-exec:" + ExecutionID`（`ExecutionID` 为空用 `TaskID`），一个任务取自消息本身（`Title`、`AgentType`、`Key`），并绑定。goal check 维持现状（不画卡片）。同一 `ExecutionID` 的重复 spawned（直发与 run-step 镜像两次送达）因 `StepID` 相同而幂等。
- `SubagentEndedMsg`：优先用 `ParentToolCallID+TaskIndex`，否则用 `cardTaskOfAgent[key]` → 写入结果、结束时间；两者都没有（只看到结束没看到开始，例如回放窗口截断）→ 建一张已结束的"无派发调用"卡片。
- subagent 的工具消息：只按 `cardTaskOfAgent[agentID]` 找卡片任务。
- 调用完成：用 `msg.ToolMeta.SubagentCall`（012）的事实——派发类卡片里**没有被生命周期事件绑定过**的任务取事实里的状态和原因；查询类卡片整张按事实画。
- 删除：`findFanoutByAgentID`、`findFanoutWithWaitingTask`、`assignFanoutTaskAgent`、`applyFanoutResults`、`settleUnreportedFanoutTasks`、`parseFanoutTasksFromMeta`（任务来自事实）、`markSubagentLifecycle` 与 `agentLifecycle`、`subagentSummary`、生命周期 `FrameStatus` 的两段构造（D11 选 A 时）。删之前 `grep -rn` 确认没有别的调用者；有就 STOP。
- continue 绑定到 continue 自己的卡片，原派发卡片保持它那次执行的结果（T6）。

### 五、卡片归属（T16）

`fanoutState` 加 `OwnerAgentID`：派发它的工具消息的 `msg.AgentID`（主 agent 为空）；"无派发调用"的卡片为空。`emitFanoutFrame` 设 `Frame.AgentID = OwnerAgentID`，于是嵌套派发的卡片进入派发它的 subagent 的视图。`agentAtScreenRow`（`reducer.go:2042` 一带）对 `FrameFanout` **先**按行查 `lineAgents`，再看 `b.frame.AgentID`，否则在 subagent 视图里点嵌套卡片的任务行会打开 subagent 自己。

### 六、路由（T1、T17）

`reduceMessage` 的工具分支：`msg.ToolMeta.SubagentCall != nil` 的消息一律进 `handleFanoutToolMessage`（替换按工具名的判断）。不再有"任务解析为空就退回普通工具卡片"的分支：没有任务的事实（`subagent_list` 开始时）画只有头部的卡片。

### 七、回放与中断（T19、T20）

- `replayTimelineWithReducer`（`commands.go:247`）开头算一次 `calls := turn.SubagentCallsInTranscript(turns)`（012），`replayToolMessage` 对 `subagent_*` 行把 `meta.SubagentCall` 设为 `calls[toolCallID]`，**不使用**落库的显示正文。
- 回合结束（`RunEndedMsg`）时，属于该回合、调用还没完成的**查询类**卡片标为 canceled 并重发帧；属于某个 subagent 的查询类卡片在那个 subagent 的 `SubagentEndedMsg` 时同样处理。派发类卡片不在回合结束时改动（`subagent_send` 的 subagent 本来就会比回合活得久）。
- plan review 的两个事件（计划 015 引入的 `plan_review_started`、`plan_reviewed`）在实时路径（`pkg/tui/notify.go` 的事件分支）和回放路径（`pkg/tui/commands.go` 的 `replaySubagentRunEventMessage`）都转成 `PlanReviewStartedMsg`/`PlanReviewedMsg`（新消息类型，放在 `notify.go` 现有消息类型旁边）；回放时它们没有工具行可锚定，按今天没有派发调用的事件的规则（`transcriptRowsBefore`，按时间）定位；reviewer 的 `subagent_spawned` 同样按时间落在它之后，绑定靠 `ParentToolCallID == ReviewID`，与位置无关。
- 回放的孤儿调用：查询类 `subagent_*` 调用画成 canceled 卡片（事实取自输入）；派发类维持现在的排除（`isFanoutToolName` 改名为 `isSubagentDispatchTool` 并只含 run/fanout/send/continue）。

### 八、删除死代码（T3、T15）

- `pkg/tui/render.go` `toolDisplayParts`（`:2513`）里八个 `subagent_*` 分支（`:2866-2959`）和 `failedActionPhrase` 里对应的七个 case——路由改完后不可能再有 `subagent_*` 的 `FrameTool`；`grep` 确认后删。
- `ActivityStatusUpdatedMsg`（`pkg/tui/notify.go:304`）及其处理分支、`FanoutTaskState.Activity`、`updateFanoutTaskActivity`、`⎿` 行；`FanoutTaskState.TokenCount`、`updateFanoutTaskTokens` 及 `TokenUsageDeltaMsg` 里重发卡片的那段（`ObserveSubagentUsage` 保留）。

### 九、任务计时（T22）

- 时钟来源：派发类任务的开始 = 绑定它的 `SubagentSpawnedMsg.Timestamp`（事件的 `CreatedAt`）；结束 = `SubagentEndedMsg` 的 `FinishedAt`（012 给 `SubagentEndedPayload` 新增的 `FinishedAtMs`，`SubagentEndedMsg` 加同名字段并在 `pkg/tui/notify.go:794`、`pkg/tui/commands.go:801` 两处构造时带上），没有这个字段的旧事件用 `Timestamp`。实时与回放取的是同一组事件字段，两边算出的最终耗时相同；被回收的执行用的是它真正停下的时间而不是回收发生的时间。查询类任务取事实的 `StartedAt`/`FinishedAt`（秒）；当事实的 `ExecutionID` 等于 reducer 正在跟踪的这个 agent 当前执行（`cardTaskOfAgent[key]` 指向的任务的执行 id），之后这个执行的 `SubagentEndedMsg` 也把查询行改成结束状态和最终耗时（reducer 为此记住每个 agent 当前执行出现过的查询行，`queryRowsOfExecution map[string][]cardTaskRef`）。
- `FanoutTaskState` 加 `ExecutionID string`、`StartedAt time.Time`、`EndedAt time.Time`。
- 计时必须在**绘制时**算，不能拼进 reducer 的内容字符串里（字符串只在事件到来时生成，计时不会走）。`Frame` 加 `FanoutLineClocks []fanoutLineClock`（与 `FanoutLineAgents` 一样按内容行对齐；`fanoutLineClock{Start, End time.Time}`，只有任务标题行有值）。`renderFanout` 画任务标题行时按 `End` 是否为零选 `formatDuration(End-Start)` 或 `formatWorkingElapsed(time.Since(Start))`，接在标题后面。
- 重画：`renderBlockFull`（`pkg/tui/reducer.go:2458`）把"有运行中计时的 `FrameFanout`"算作随时间变化的块，缓存键用**当前秒数**（不是 `spinnerPhase`，计时一秒才变一次，不必每 120ms 重排一次）。驱动重画的定时器复用压缩卡片那一个：`hasRunningCompactLocked`（`:3270`）改名 `hasLiveBlockLocked`，条件加上"当前视图里有运行中计时的 `FrameFanout`"；`syncCompactAnimationLocked` 改名 `syncLiveBlockAnimationLocked`，`RenderFrame` 里对 `FrameFanout` 也调用它（现在只对 `FrameMemoryCompact` 调）。这样主回合结束后，`subagent_send` 派发的 subagent 还在跑时计时照样走。

### 十、上个进程没跑完的执行（T23）

- 不在界面上判断"这个执行还活不活"。SRT-004 的回收器会为它发 `subagent_ended`（`Status:"failed"`、`Error: AbandonedRunReason`、`FinishedAtMs` = 运行被盖章的结束时间）；卡片按普通的 failed 收尾：`✗`、原因行是这句原文、标题后是最终耗时。本计划对 T23 不需要额外的产品代码，只需要第九节的 `FinishedAtMs` 和第 1 步第 14 条测试守住"用的是载荷里的结束时间"。
- SRT-004 尚未完成时，旧会话里这类任务仍会停在运行中并持续计时——这是 SRT-004 要修的既有缺陷的表现，不要在本计划里加兜底；T23 的真机检查放在计划 009 里做（009 依赖 SRT-004 之后的状态）。

## 现状摘录（`bda9505`）

- `pkg/tui/reducer.go` `reduceMessage` 工具分支：

  ```go
  toolName := strings.TrimSpace(msg.ToolName)
  // Route subagent_fanout and subagent_run to the fanout display path.
  if toolName == "subagent_fanout" || toolName == "subagent_run" {
  	return r.handleFanoutToolMessage(msg, toolName)
  }
  ```

- `pkg/tui/reducer.go` `SubagentSpawnedMsg` 分支的匹配顺序：`fanoutTaskForLifecycle(m.ParentToolCallID, m.TaskIndex)` → `findFanoutByAgentID(agentID)` → `findFanoutWithWaitingTask()` + `assignFanoutTaskAgent` → `markSubagentLifecycle` + `title := "subagent spawned"` 的 `FrameStatus`。`SubagentEndedMsg` 分支对称。
- `pkg/tui/reducer.go` `renderFanoutSummary`：`return fmt.Sprintf("%s %d%s tasks…", verb, n, typeLabel)`。
- `pkg/tui/reducer.go` `emitFanoutFrame`：`Frame{Kind: FrameFanout, StepID: fs.StepID, RunID: runID, Final: !fs.Running, Content: content, FanoutLineAgents: lineAgents, Summary: renderFanoutSummary(fs), Duration: fs.Duration}`（没有 `AgentID`）。
- `pkg/tui/render.go` `renderFanout`：`styled := lipgloss.NewStyle().Faint(true).Render(p + wl)`。
- `pkg/tui/reducer.go` `replaceOrAppendBlock` 按 `Kind + StepID` 替换块（同一张卡片必须始终是 `FrameFanout`，否则会出现两块）。
- 测试先例：`pkg/tui/reducer_test.go` 里断言 `Ran 1 tasks · 0 done, 1 failed`（`:587`）和 `Running 1 tasks…`（`:1143`）的两个测试；`pkg/tui/chat_session_test.go` 里点击 fanout 行打开视图的 `TestRosterCursorFollowsTheViewOpenedByClickingACard`（用 `fanoutFrameForTest()`）；`pkg/tui/commands_test.go` 的回放测试。

## 缓存影响

无。只改 TUI 的显示状态机和渲染；不改任何发给模型的内容。完成后用第 7 步的真机会话从状态库算一次主会话命中率，与 README 记录的基线比较，不得下降。

## 范围

**只改这些文件：** `pkg/tui/reducer.go`、`pkg/tui/render.go`、`pkg/tui/notify.go`、`pkg/tui/commands.go`、`pkg/tui/tty_session.go`（`blockLineCache` 加计时用的缓存键）、`pkg/tui/chat_surface.go`（只删 `notifyPlanReviewFailure` 在卡片已说明时的那条提示）、以及 `pkg/tui/reducer_test.go`、`pkg/tui/render_test.go`、`pkg/tui/chat_session_test.go`、`pkg/tui/commands_test.go`、`pkg/tui/tty_session_test.go`（如需）。

**不要动：** `pkg/tool/**`、`pkg/run/**`、`pkg/turn/**`（012 已提供所需的一切；发现还缺什么就 STOP）；roster 的光标与行文字（001、010）；subagent 视图内部（007）；`pkg/gateway/**`、`frontend/**`（014）。

## 步骤

### 第 1 步：先写失败的测试

在 `pkg/tui/reducer_test.go` 加（每条都用 `Reducer` 喂消息、取最后一帧 `FrameFanout`；工具消息的 `ToolMeta` 用 `tool.BuildToolMeta(tool.StepEvent{…})` 造，这样 `SubagentCall` 来自 012 的真实推导）：

1. `TestSendCardFollowsItsAgentToTheEnd`：send 开始 → `SubagentSpawnedMsg{ParentToolCallID: send 的 StepID, TaskIndex: 0}` → 该 agent 的两条工具消息 → send 完成（结果 JSON 同 012"现状摘录"）→ `SubagentEndedMsg{Status:"ok"}`。断言：全程没有 `FrameStatus`；最终 `Summary` 以 `Ran 1 general-purpose task in background · ` 开头；`Content` 含 `✓ 计划001 Go车道实施` 和 `… +1 tool uses`，不含 `{`，也没有任何一行去掉首尾空白后以 `· ` 开头、以 `tool uses` 结尾（T21）。
2. `TestQueryCardsShowTaskRowsNotJSON`：status / wait（含 `timed_out`）/ close / list（三条记录、一条空列表）/ continue 各一个子测试，断言头部与第二节表格一致、行文字与第三节一致、`Content` 不含 `{`、不含记录里的 prompt 文本。
3. `TestRunCardNamesOneTaskInTheSingular`：`Running 1 general-purpose task…`、`Ran 1 general-purpose task`。同时把 `:587`、`:1143` 两处旧断言改成正确的单数（报告里写明）。
4. `TestFanoutCardTakesSkippedOutcomesFromTheCallFacts`：两个任务，一个 prompt 为空；只有一个 spawned/ended；完成后断言空任务行是 `! (empty task)` 加 `skipped: empty prompt`，头部 `· 1 done, 1 skipped`。
5. `TestFanoutCardShowsWhyTheCallFailed`：完成消息 `ToolMeta.Status="failed"`、`StepEvent.Error="subagent capacity is 0; main agent must execute this fanout directly"`；断言头部 `Failed to run 2 general-purpose tasks`，正文第一行是这句错误。
6. `TestCancelledTasksAreCountedOnTheCard`：三个任务，两个 `cancelled`；断言头部 `· 1 done, 2 cancelled`，取消的任务行带 `×`。
7. `TestSpawnWithoutItsCallNeverJoinsAnotherCard`：一张两个任务都在等待的 fanout 卡片，再来一个 `ParentToolCallID` 为别的调用的 spawned；断言 fanout 两行标题不变、`FanoutLineAgents` 不含这个 agent，且出现一张 `StepID` 以 `subagent-exec:` 开头的新卡片。
8. `TestContinueLeavesTheOriginalCardAlone`：fanout 跑完后对第 2 个任务 continue（spawned 的 `ParentToolCallID` = continue 的 StepID、`TaskIndex`=0）；断言 fanout 卡片的 `Content` 不变，continue 卡片 `Continued 1 general-purpose task`。
9. `TestNestedDispatchCardBelongsToItsAgentView`：`msg.AgentID="task-a"` 的 `subagent_run` 开始消息；断言产出的 `FrameFanout.AgentID == "task-a"`。
10. `TestRuntimeDispatchedAgentGetsACardNotStatusLines`：`ParentToolCallID` 为空的 plan-reviewer spawned/ended；断言没有 `FrameStatus`，有一张 `Ran 1 plan-reviewer task` 卡片，行文字以 `✓ Plan review` 开头（这是没有 `plan_review_started` 的情况，例如旧会话）。
11. `TestSettledTaskHasNoToolUseStatsLine`：fanout 两个任务各跑 3 次工具后一个 done、一个 failed（带 `Error`）；断言 `Content` 里不含 `· 3 tool uses`，failed 任务的原因行仍在，两个任务都还有 `… +2 tool uses`（T21）。
12. `TestTaskRowsShowLiveAndFinalElapsed`：fanout 两个任务，spawned 的 `Timestamp` 分别为 t0、t0+1s，第二个在 t0+75s ended；用 `emitFanoutFrame` 取帧，断言第二个任务的 `FanoutLineClocks` 为 `{t0+1s, t0+75s}`、第一个 `End` 为零；再用渲染器在固定的"现在"（注入 `time.Now` 的替身，若 `pkg/tui` 没有现成的时钟注入点，则直接测 `renderFanout` 用的那个格式化函数并传入时间）渲染，断言两行分别以 ` · 1m14s`、` · <formatWorkingElapsed 的结果>` 结尾；单任务的 `subagent_run` 卡片结束后头部**不含**时长。
13. `TestQueryRowFollowsItsExecutionToTheEnd`：`subagent_status` 结果说运行中（`ExecutionID` 与已绑定的派发任务相同），之后该执行 ended；断言 status 卡片那一行变为 `✓` 加最终耗时，短语行不变。
14. `TestAbandonedExecutionEndsAtItsRealStopTime`：spawned 的 `Timestamp` 为 t0，ended 的 `Timestamp` 为 t0+2h（回收发生的时间）、`FinishedAt` 为 t0+30s、`Status:"failed"`、`Error:"The process running this turn stopped before it finished."`；断言任务行是 `✗`、标题后是 ` · 30s`（`formatDuration(30*time.Second)` 的结果），不是两小时，原因行是这句原文。
15. `TestPlanReviewCardUsesTheDispatchVerbs`：`PlanReviewStartedMsg{ReviewID:"rv-1"}` → 头部 `Starting 1 plan-reviewer task…`；`SubagentSpawnedMsg{ParentToolCallID:"rv-1", TaskIndex:0}` → `Running 1 plan-reviewer task…`，且没有另开 `subagent-exec:` 卡片；ended → `Ran 1 plan-reviewer task`。另一条：`PlanReviewStartedMsg{ReviewID:"rv-2"}` 后直接 `PlanReviewedMsg{ReviewID:"rv-2", Outcome:"failed", Error:"model zhipuai/glm-5.3-flash is not configured for this session"}` → 头部 `Failed to start 1 plan-reviewer task`，下一行是这句错误原文，且 UI 没有收到 `Plan review by …` 那条提示。

在 `pkg/tui/render_test.go` 加 `TestSubagentCardBodyIsFullBrightness`：渲染一张 fanout 卡片，断言输出不含 `\x1b[2m`。

在 `pkg/tui/commands_test.go` 加 `TestReplayedSubagentSendCardIsBuiltFromFacts`：照该文件已有的回放测试搭建 transcript（assistant 行的 `tool_calls` 含一次 `subagent_send`，tool 行的 content 是 send 的结果 JSON，`tool_display` 正文故意是旧的 `output:\n\n```json\n{…}` 形状），events 里有带 `parent_tool_call_id` 的 spawned/ended；断言回放出的屏幕文字含 `Ran 1 general-purpose task in background`、不含 `"agent_id"`。

**验证**：`CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -run 'TestSendCardFollowsItsAgentToTheEnd|TestQueryCardsShowTaskRowsNotJSON|TestRunCardNamesOneTaskInTheSingular|TestFanoutCardTakesSkippedOutcomesFromTheCallFacts|TestFanoutCardShowsWhyTheCallFailed|TestCancelledTasksAreCountedOnTheCard|TestSpawnWithoutItsCallNeverJoinsAnotherCard|TestContinueLeavesTheOriginalCardAlone|TestNestedDispatchCardBelongsToItsAgentView|TestRuntimeDispatchedAgentGetsACardNotStatusLines|TestPlanReviewCardUsesTheDispatchVerbs|TestSettledTaskHasNoToolUseStatsLine|TestTaskRowsShowLiveAndFinalElapsed|TestQueryRowFollowsItsExecutionToTheEnd|TestAbandonedExecutionEndsAtItsRealStopTime|TestSubagentCardBodyIsFullBrightness|TestReplayedSubagentSendCardIsBuiltFromFacts' -count=1` → 全部失败。有任何一条不失败，在报告里说明原因；第 7、9、10 条不失败就 STOP（说明根因判断与代码不符）。

### 第 2 步：路由与卡片状态（设计第四、五、六节）

改 `handleFanoutToolMessage`、`fanoutState`/`FanoutTaskState`、`SubagentSpawnedMsg`/`SubagentEndedMsg` 分支、工具消息分支，按设计删除旧函数。

**验证**：第 1 步的 1、2、4、6、7、8、9、10 条转绿；`grep -n "findFanoutWithWaitingTask\|assignFanoutTaskAgent\|applyFanoutResults\|settleUnreportedFanoutTasks\|findFanoutByAgentID\|markSubagentLifecycle\|subagentSummary" pkg/tui/*.go` → 只剩测试里对它们"已删除"无关的引用（应为无输出；测试里若还有调用就改测试）。

### 第 3 步：卡片头、任务行、计时与中断的执行（设计第二、三、九、十节）

把 `renderFanoutSummary` 换成按 `Verb` 查表的头部函数，`renderFanoutContent` 按派发类/查询类出行。表格写成一个 `map[string]…` 或 `switch`，**不要**在多处各拼一份字符串。

**验证**：第 1 步 3、5、11、12、13、14、15 条转绿（12、14 需要第九节，13 需要第九节的查询行跟随——第九节在本步与第二、三节一起做）；`CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -count=1` → `ok`（改了哪些既有断言，报告里逐条列出：只允许改"复数"、"lifecycle 文字行已不存在"、"正文不再 faint"、"没有 `· N tool uses` 统计行"、"plan review 的失败改由卡片说明"（`TestPlanReviewFailureKeepsTheApprovalAndReportsIt`，`pkg/tui/chat_session_test.go:12556`，改为断言卡片头 `Failed to start …` 或任务行 `✗` 与原因，且审批仍挂起）五类，别的既有测试失败就 STOP）。

### 第 4 步：渲染亮度与点击（设计第一、五节）

去掉 `renderFanout` 的 `Faint(true)`；`agentAtScreenRow` 对 `FrameFanout` 先查行。

**验证**：`TestSubagentCardBodyIsFullBrightness` 转绿；`CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -run 'TestRosterCursorFollowsTheViewOpenedByClickingACard' -count=1` → `ok`。

### 第 5 步：回放、中断、孤儿调用（设计第七节）

**验证**：`TestReplayedSubagentSendCardIsBuiltFromFacts` 转绿；再加 `TestInterruptedWaitCardSettlesAsCanceled`（主回合中 wait 开始后 `RunEndedMsg`，断言卡片头以 `Canceled ` 开头、`Final`）并通过。

### 第 6 步：删除死代码（设计第八节）

**验证**：
- `grep -rn "ActivityStatusUpdated\|updateFanoutTaskActivity\|updateFanoutTaskTokens\|TokenCount " pkg/tui/*.go` → 无输出
- `grep -n '"subagent_' pkg/tui/render.go` → 无输出
- `$(go env GOPATH)/bin/deadcode -tags fts5 ./... ` 与开工前基线一致或更少（报告里列出减少的条目）
- `CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -count=1` → `ok`

### 第 7 步：真机（tmux），假模型 + 真实模型

假模型（确定性，覆盖 run/fanout/空 prompt/失败）：

```bash
D=.claude/skills/run-forebrain/driver.sh
$D reset
$D start tool '[{"name":"subagent_fanout","arguments":{"max_parallel":2,"tasks":[{"title":"任务16 migrate 导入","prompt":"run the command","subagent_type":"general-purpose"},{"title":"空任务","prompt":"","subagent_type":"general-purpose"}]}},{"name":"shell","arguments":{"command":"sleep 5"}}]'
$D submit 'fan out'
$D wait 'Running 2 general-purpose tasks' 30
$D screen
$D wait 'Ran 2 general-purpose tasks' 60
$D screen
```

断言屏幕：头部单复数正确；已结束的任务下面没有 `· N tool uses` 统计行；运行中的任务名后面有计时，隔 3 秒再 `$D screen` 一次，计时前进了约 3 秒；结束的任务名后面是最终耗时；空任务行 `! (empty task)` 和 `skipped: empty prompt`；没有任何 `subagent … spawned` 文字行；点任务行（`$D click <col> <row>`，命令由计划 001 加入）进入它的视图。

真实模型（智谱，按 README 全局规则 11 配置隔离 `FOREBRAIN_HOME`，密钥只用 `${FOREBRAIN_E2E_ZHIPU_KEY}` 引用）：提交

> 用 subagent_send 派发一个 general-purpose subagent（title 写"读 README"），让它读 README.md 并用三句话总结。然后依次调用 subagent_status、subagent_list、subagent_wait（timeout_ms 设 1000），最后调用 subagent_close 停掉它。

每张卡片出现后 `$D screen`。断言：send 卡片的任务行在主回合结束后计时仍在走（回合结束后等 5 秒再截一次）；五张卡片都没有 `{`、没有原始 task id、没有 prompt 原文；send 卡片在 subagent 结束（或被 close 停掉）后变成 `Ran …`/带 `×`；wait 卡片在超时时写 `still running when the wait ended`；主视图没有 `spawned`/`ended` 文字行。再 `/resume` 回到这个会话，截图回放后的样子与实时一致（最终耗时逐字相同）。T23 的真机检查（杀掉进程后恢复）依赖 SRT-004，放在计划 009 第 5 步做。

最后用智谱再跑一次截图里的场景之一：让主 agent 用 `subagent_run` 派发一个 general-purpose subagent，subagent 在任务里再调用一次 `subagent_run`（提示词里明确要求它这样做），截图：嵌套卡片只出现在那个 subagent 的视图里（`$D click` 进入后可见），主视图没有。

**验证**：把每一张截图（`$D screen` 的文字输出）贴进报告，逐条对应 T1–T23。从状态库算这次会话主 agent 的命中率，记录在报告里。

## 测试计划

第 1 步列出的 18 条新测试 + 第 5 步 1 条；全部放在与被测生产文件同名的测试文件里（架构测试要求）。结构参照 `pkg/tui/reducer_test.go` 里现有的 fanout 测试和 `pkg/tui/commands_test.go` 的回放测试。

## 完成标准

- [ ] 第 1–7 步的验证全部通过；`CGO_ENABLED=1 go test -tags fts5 ./... -count=1` 全部 `ok`
- [ ] `gofmt -l pkg cmd` 无输出；`go vet -tags fts5 ./...` 退出码 0；架构测试、包依赖图通过；死代码不增加
- [ ] `grep -rn 'Faint(true)' pkg/tui/render.go` 的结果里没有 `renderFanout` 内的行
- [ ] 第 7 步的截图逐条覆盖 T1–T23 并附在报告里
- [ ] README 状态行已更新

## STOP 条件

- 前置检查不满足。
- 第 1 步的第 7、9、10 条没有失败。
- 需要改 `pkg/tool`、`pkg/run`、`pkg/turn` 才能完成（说明 012 漏了什么，回来补 012）。
- 删除某个旧函数时发现它还有本计划之外的调用者。
- 真实模型下某张卡片仍出现 JSON、原始 task id 或 prompt 原文。
- 主会话命中率下降。

## 维护说明

- 新增 `subagent_*` 工具时：012 的推导函数加分支，本计划第二节的头部表加一行，第 1 步第 2 条的子测试加一个。
- 卡片绑定只认 `ParentToolCallID+TaskIndex` 和 `cardTaskOfAgent`。任何时候想加"按顺序猜"之类的回退，先回来问：那正是 T10 的根因。
- 计划 005 的用户发起执行（`Origin="user"`）没有派发调用，按本计划会得到一张"无派发调用"的卡片；它在主视图里该不该出现由计划 007 决定（D3 只约束了"不告诉主 agent"，没有约束界面），007 执行时在这里补规则并加测试。
- 计划 011 的自动继续经由 005 的通道发起，同上。
