# 计划 012：subagent_* 工具调用的"卡片事实"只在引擎里推导一份

> **执行者须知**：逐步执行。每一步都运行"验证"命令并确认结果后再进入下一步。出现"STOP 条件"里的任何情况，立即停止并报告。
> 完成后更新 `docs/plan/SUBAGENT_CONVERSATION/README.md` 里本计划的状态行。**不要提交代码。** 先读 README 的"全局规则"。
>
> **前置检查**：README 里计划 010 必须是 `DONE`（本计划要用 010 引入的 `tool.SubagentTaskTitle`），否则 STOP。决策 D10、D11 必须已由 owner 定夺（README 决策表"结论"列不是"待定"），否则 STOP。
>
> **漂移检查（先运行）**：
> `git diff --stat bda9505 -- pkg/run/subagent.go pkg/tool/format.go pkg/tool/notification.go pkg/event/tool_events.go pkg/event/run_events.go pkg/turn/compaction.go pkg/state/schema_migrations.go pkg/state/schema.sql`
> 计划 010 会改 `pkg/run/subagent.go`、`pkg/tool/format.go`（`SubagentTaskTitle`），那是预期的；其余有改动时，按函数名和注释原文核对"现状"摘录，对不上就 STOP。

## 状态

- **优先级**：P1
- **工作量**：L
- **风险**：MED（改动显示用的共享数据结构，外加一次只改数据、不改结构的状态库迁移）
- **依赖**：010
- **类别**：bug
- **基线**：提交 `bda9505`，2026-10-05

## owner 的要求（逐字）

> （附两张 TUI 截图）如 forebrain harness tui截图所示，这两个subagent_*工具的消息卡片不符合需求，output区域是原始json数据，必须定位根因并修复bug，对齐subagent_fanout的消息卡片的UI/UX，重新设计，必须在docs/plan/SUBAGENT_CONVERSATION目录下生成新计划文件

> 必须全面审计所有的subagent_*工具的消息卡片的UI和UX，找出所有问题，定位根因并设计修复计划，加入本期计划中

> （2026-10-05，看过预览后）✓ 任务16 migrate 导入、✗ 任务17 扩展目录和计划002 Web车道实施这样的任务名称后面加上运行时长计时和最终耗时
>
> 运行中的加上运行时长计时，执行结束的（包括成功、失败、取消等）的加上最终耗时

截图一：`● Sent agent 计划001 Go车道实施 general-purpose · <0.1s`，output 区域是 `{"output": "{\"agent_id\":…}"}` 的 JSON 代码块，卡片下面一行 `subagent general-purpose spawned general-purpose [subagent-6e5c042d-…]`。截图二：`● Agent status subagent-6e5c042d-… <0.1s`，output 区域是整条注册表记录的 JSON（含整段派发 prompt）。

## 本计划在三份计划里的位置

审计结论和三份计划的分工见 README 的"subagent_* 工具卡片审计"一节。本计划只做两个界面共用的那一层：

- 引擎在派发时漏记、记错的关联信息（A1、A2）；
- 每个 `subagent_*` 调用"关于哪些任务、它们各是什么状态"的**卡片事实**，由一个 Go 函数从调用的输入和模型看到的结果推导出来，随工具事件、持久化的工具元数据和历史接口送到两个界面（A3、A4、A5）；
- 给通知钩子、持久化的 `tool_display` 用的文字正文和摘要不再是 JSON，也不再含 prompt（A6、A7）；
- 旧会话里 `subagent_send` 的生命周期事件缺的关联信息用一次数据迁移补上（A8，D10）。

TUI 怎么画（计划 013）、网页怎么画（计划 014）不在这里。

## 根因（逐条，含证据）

- **A1：`subagent_send` 派发的 subagent 不知道是哪次调用派发的它。** `pkg/run/subagent.go:1487` 起，`subagent_send` 为异步执行新建了一个脱离调用的上下文 `baseCtx := context.Background()`，只把会话 id、缓存键、父运行 id 带过去，**没有带工具调用 id**。`prepareSubagentExecutionResolved`（`:886`）的 `ParentToolCallID: strings.TrimSpace(tool.ToolUseIDFromContext(baseCtx))` 因此永远是空串，`subagent_spawned`/`subagent_ended` 事件的 `parent_tool_call_id` 也是空串。本机状态库核实：截图里的 `计划001 Go车道实施` 以及 2026-09-28 的 `M1`–`M8` 全部 9 个 `subagent_send` 派发，其生命周期事件都没有 `parent_tool_call_id`；同期 `subagent_run`/`subagent_fanout` 的都有。后果：两个界面都没法把这个 subagent 和派发它的卡片对上，只能另画一行生命周期文字（截图一最后一行）；TUI 还会把它误挂到任何一个仍在派发的 fanout 卡片的等待行上（计划 013 的 T10）；回放时它的事件只能按时间猜位置（`pkg/tui/commands.go` 的 `subagentEventAnchors` 找不到锚点，落到 `transcriptRowsBefore`）。
- **A2：`subagent_continue` 的生命周期事件里"派发调用"和"任务序号"自相矛盾。** `continueSubagentExecution`（`pkg/run/subagent.go:1200`）里 `lifecycle := record`（`:1233`）复制了原记录，随后只把 `ParentToolCallID` 改成本次 continue 调用的 id（`:1235`），`TaskIndex` 却保留了原派发里的序号（例如 fanout 的第 3 个任务是 2）。`final`（`:1255`）同理。一个调用 id 配上另一个调用里的序号，界面按"调用 id + 序号"精确匹配时永远对不上 continue 自己的卡片（它只有一个任务，序号 0）。
- **A3：六个生命周期工具没有任何专门的显示逻辑，输出区域落到通用格式化器。** `pkg/tool/format.go:27` 的 `FormatToolStepResult` 只给 `subagent_fanout`（`:84`）和 `subagent_run`（`:86`）配了格式化器；`subagent_send`、`subagent_status`、`subagent_wait`、`subagent_continue`、`subagent_close`、`subagent_list` 全部落到 `formatGenericToolStep`（`:1068`），后者把输出 map 原样 `json.MarshalIndent` 成 `output:` + ```` ```json ```` 代码块。而这些工具返回给模型的是**一个 JSON 字符串**，引擎把它包成 `{"output": "<字符串>"}`，所以界面上看到的是 JSON 里套着转义过的 JSON（截图两张都是）。状态库里这次调用的事件核实：`display_body` 就是这段 `output:\n\n```json\n{"output": "{\"agent_id\"…`。这是两个界面共同的根因：TUI 和网页都直接显示 `display_body`。
- **A4：六个生命周期工具的调用标签和摘要是原始输入 JSON。** `toolInvocationLabel`（`:2138`）对没有专门标签的工具走"Generic fallback: full input JSON"（`:2154`），得到 `subagent_send {"subagent_type":…,"task":"<整段 prompt>",…}`；`SummarizeToolStep`（`:215`）再加上 `ran `。状态库核实：截图那次 `subagent_send` 的 `summary` 是 `ran subagent_send {"subagent_type":"general-purpose","task":"你在 /Users/doudou/…（整段 prompt）`。网页的工具卡片标题就是 `summary`（`frontend/src/composables/useChatStream.ts` 的 `subagentStepSummary`），所以网页标题本身就是一段带整段 prompt 的 JSON。调用标签还被 TUI 用作 subagent 卡片第三层的"最近工具"文字（`pkg/tui/reducer.go` 的 `fanoutToolLabel` 读 `ToolMeta.Invocation`），嵌套派发时也会露出 JSON。
- **A5：卡片需要的结构化事实只存在于一段显示文字里，两个界面各自去猜。** TUI 的 fanout 卡片在调用结束时用 `applyFanoutResults(fs, msg.Content)`（`pkg/tui/reducer.go`）去解析 `{"results":[…]}`，但实时路径上 `msg.Content` 是 `display_body`（`pkg/tui/notify.go:768` `msg.Msg.Content = p.DisplayBody`），对 fanout 来说是 `formatFanoutStep` 生成的 `3 tasks (general-purpose):\n1. …` 这样的文字，**永远解析不出来**；随后 `settleUnreportedFanoutTasks` 把所有没收到结束事件的任务一律标成 `done`。于是空 prompt 被跳过的任务、`fail_fast` 跳过的任务在实时界面上显示成 `✓`（回放时 `display_body` 若是旧的原始 JSON 才偶尔对）。网页则根本没有这些事实，只能显示 `display_body` 文字。没有一份"这次调用涉及哪些任务、各自什么结果"的结构化数据，是 TUI、网页、实时、回放四处不一致的共同根因。
- **A6：`subagent_run`/`subagent_fanout` 的正文把派发 prompt 放进了主对话。** `formatSubagentRunStep`（`:2036`）的正文是 `subagent type: X\n\n<整段 prompt>`；`formatFanoutStep`（`:1992`）每个任务写 `标题 — prompt 片段`，片段按字节截到 97 再加 `...`。工具参数的说明写着 "Shown in the agent roster and on the task card; the prompt itself is never shown there"（`pkg/run/subagent.go:86`），owner 的规矩也是"prompt 是 subagent 自己视图的第一条消息"。TUI 的卡片不显示这段正文，所以只有网页、通知钩子和持久化的 `tool_display` 露出了 prompt。状态库核实：75 条 `subagent_*` 工具行全部存了 `tool_display`，`subagent_run` 的正文就是整段 prompt。
- **A7：`subagent_run`/`subagent_fanout` 的调用标签会截断。** `subagentRunLabel`（`:2273`）用 `truncateSummary` 截标题或 prompt，状态库里能看到 `subagent (explore) "在仓库 … 中只读追踪 TUI 默认模型选择器的数据来源。定..."`。owner 的规矩：卡片头部展示调用参数不得截断。
- **A8（D10）：A1 修好之后，旧会话里已经落库的 `subagent_send` 生命周期事件仍然没有 `parent_tool_call_id`。** 它们缺的正是 A1 漏记的那条关联；不补，旧会话重放时 `subagent_send` 的卡片永远对不上它的 subagent（计划 013/014 只按精确关联绑定，不再按"第一个等待的任务"去猜）。可以精确补上：`subagent_send` 返回给模型的结果里有 `run_id`，它等于该 subagent 第一次执行的 `execution_id`。本机状态库用只读查询核实过：按"同会话、`tool_name='subagent_send'` 的 `tool_call_completed` 事件的结果 `run_id` = 生命周期事件的 `execution_id`"关联，`M1`–`M8` 与 `计划001` 的 18 条 spawned/ended 事件全部唯一命中，两次 goal check 和一条 2026-09-07 的旧事件不命中（它们本来就没有派发调用）。

## 现状摘录（`bda9505`）

- `pkg/run/subagent.go:1487-1499`（`subagent_send` 处理函数里）：

  ```go
  baseCtx := context.Background()
  if sid := strings.TrimSpace(llm.AgentSessionIDFromContext(ctx)); sid != "" {
  	baseCtx = llm.WithAgentSessionID(baseCtx, sid)
  	baseCtx = tool.WithConversationSessionID(baseCtx, sid)
  }
  if cacheKey := strings.TrimSpace(llm.PromptCacheKeyFromContext(ctx)); cacheKey != "" {
  	baseCtx = llm.WithPromptCacheKey(baseCtx, cacheKey)
  }
  if parent := strings.TrimSpace(tool.RunIDFromContext(ctx)); parent != "" {
  	baseCtx = tool.WithRunID(baseCtx, parent)
  }
  taskID := newSubagentTaskID()
  entry, err := spawnAsyncSubagent(baseCtx, fac, taskID, title, task, subType)
  ```

- `pkg/run/subagent.go:1233-1237`：

  ```go
  lifecycle := record
  lifecycle.ExecutionID = executionID
  lifecycle.ParentToolCallID = strings.TrimSpace(tool.ToolUseIDFromContext(ctx))
  lifecycle.Task = msg
  lifecycle.Status = agent.StatusRunning
  ```

- 各工具返回给模型的结果形状（**本计划不改它们**，只读）：
  - `subagent_run`：`{agent_id, task_id, run_id, parent_run_id, session_id, query_source, status:"ok", output, finished_at, agent_type, agent_kind, runtime_kind, definition_source, one_shot, continuable}`；空 prompt 时 `{status:"skipped", error:"skipped: empty prompt"}`。
  - `subagent_fanout`：`buildFanoutSummary`（`:1712`）→ `{summary:{total,succeed,failed,finished}, results:[{index, task, subagent_type, output, error, ok}]}`；跳过的任务 `error` 以 `skipped` 开头（`skipped: empty prompt`、`skipped due to fail_fast`）。
  - `subagent_send`：`{agent_id, task_id, run_id, parent_run_id, session_id, worker_session_id, query_source, status, started_at, agent_kind, agent_type, runtime_kind}`；空 prompt 同 `subagent_run`。
  - `subagent_status`：一条 `agent.HistoryEntry` 的 JSON（字段见 `pkg/agent/subagent_history.go:22-48`：`task_id`、`title`、`task`、`status`、`output`、`error`、`started_at`、`updated_at`、`finished_at`、`agent_type`…）。`status` 取值 `running`/`ok`/`failed`/`cancelled`（`:15-18`）。
  - `subagent_wait`：`{record: HistoryEntry, timed_out?: true}`。
  - `subagent_continue`：`{record: HistoryEntry}`。
  - `subagent_close`：`{status:"cancel_requested", record?: HistoryEntry}`。
  - `subagent_list`：`{records: [HistoryEntry…]}`。
  - 引擎把工具返回的字符串放在 `StepEvent.Output["output"]`（字符串）里；出错时 `StepEvent.Error` 非空。
- `pkg/tool/notification.go:279` `ToolMeta` 与 `pkg/event/tool_events.go:62` `ToolCallMeta` 是同一组字段的两份结构；`RunEventFromStep`（`pkg/tool/notification.go:19`）逐个字段把前者抄进后者，新增字段必须两边都加、并在这里抄过去。
- `pkg/tool/format.go:123` `BuildToolMeta(evt StepEvent) ToolMeta`：实时路径（TUI 的 `pkg/tui/notify.go`、网页的 `pkg/gateway/server.go:1815/:1838`、事件投影 `:1911`）都经过它；它的结果还被持久化进行的 `tool_display` 部分的 `tool_meta_json`。
- `pkg/turn/compaction.go:122` `GoalPosition`、`:197` `RunWorkedLines`：两个"从落库的行推导显示事实"的共享函数，TUI 回放（`pkg/tui/commands.go:251` 一带）和网页历史接口（`pkg/gateway/api_extra.go:1155` `handleChatMessages`）都调用它们。本计划新增的历史推导函数照这个先例放在同一个文件里。
- 包约束（README 全局规则 4、5）：`pkg/tool`、`pkg/run`、`pkg/turn`、`pkg/state` 都已是 20 个生产文件，**不得新建生产文件**；不得新增包间 import。`pkg/tool` 不 import `pkg/agent`（这就是计划 010 把 `SubagentTaskTitle` 放进 `pkg/tool` 的原因），所以本计划里 `pkg/tool` 不能用 `agent.Status*` 常量，用字符串字面量并在注释里指向 `pkg/agent/subagent_history.go:15-18`。
- 状态库迁移框架：`pkg/state/schema_migrations.go:16` `stateSchemaVersion = 6`，迁移表 `:36-43`（V1–V6；最近的一条 `migrateStateV5ToV6` 在 `:120`，`migrateStateV4ToV5`（给 `fb_runs` 加 `owner`）在 `:72`，`migrateStateV3ToV4` 在 `:51`）；测试先例 `pkg/state/schema_migrations_test.go:1303` `TestStateV3UpgradesToV4WithSessionsIntact`。

## 设计

### 1. 卡片事实的数据结构（`pkg/event/tool_events.go`）

在 `ToolCallMeta` 旁边新增，并给 `ToolCallMeta` 加字段 `SubagentCall *SubagentCall \`json:"subagent_call,omitempty"\``：

```go
// SubagentCall is what one subagent_* tool call is about — the tasks it
// dispatched, checked, waited for, stopped or listed — decoded once from the
// call's input and from the result the model received, so the terminal and
// the web draw the same card from the same facts. It never carries a prompt or
// a subagent's answer: those belong in the subagent's own view.
type SubagentCall struct {
	// Verb is what the call does to its tasks: "run" (subagent_run and
	// subagent_fanout), "send", "continue", "status", "wait", "close", "list".
	Verb  string             `json:"verb"`
	Tasks []SubagentCallTask `json:"tasks"`
}

// SubagentCallTask is one task a subagent_* call is about.
type SubagentCallTask struct {
	// Index is the task's position in the call: tasks[i] of a fanout, the
	// i-th record of a list, 0 otherwise. A dispatch's lifecycle events carry
	// the same index, which is how a surface binds the task to its agent.
	Index int `json:"index"`
	// Key is the roster key of the agent the task runs as (agent.RosterKey),
	// when the input or result names it; empty for a task that has not
	// started or never will.
	Key       string `json:"key,omitempty"`
	Title     string `json:"title,omitempty"`
	AgentType string `json:"agent_type,omitempty"`
	// Status is one of "waiting", "running", "done", "failed", "cancelled",
	// "skipped", or "" when the call has not said yet.
	Status string `json:"status,omitempty"`
	// Error is why a task failed or was skipped, as the engine described it.
	Error         string `json:"error,omitempty"`
	TimedOut      bool   `json:"timed_out,omitempty"`      // subagent_wait returned first
	StopRequested bool   `json:"stop_requested,omitempty"` // subagent_close asked it to stop
	// ExecutionID names the execution the result describes (one agent runs
	// once per dispatch and once per continue). A surface uses it to keep a
	// row's clock on the same execution the lifecycle events report.
	ExecutionID string `json:"execution_id,omitempty"`
	// StartedAt and FinishedAt are that execution's clock as the result
	// reported it, unix seconds; every task row shows its elapsed time from
	// them (running: live from StartedAt; ended: FinishedAt - StartedAt).
	StartedAt  int64 `json:"started_at,omitempty"`
	FinishedAt int64 `json:"finished_at,omitempty"`
}
```

`pkg/tool/notification.go` 的 `ToolMeta` 加同名字段 `SubagentCall *event.SubagentCall \`json:"subagent_call,omitempty"\``；`RunEventFromStep` 的 `eventMeta` 抄过去。

### 2. 推导函数（`pkg/tool/format.go`，只有这一份）

```go
// SubagentCallFromStep derives the card facts of a subagent_* call from its
// input and, once it has settled, from the result the model received. ok is
// false for any other tool.
func SubagentCallFromStep(evt StepEvent) (*event.SubagentCall, bool)
```

`BuildToolMeta` 末尾调用它并填 `meta.SubagentCall`。规则（表里"类型"指 `subagent_type`，空表示 fork，事实里写成 `"fork"`；"标题"一律经 `SubagentTaskTitle(title, prompt)`，记录里的 `title` 已经是引擎用它算好的，直接用）：

| 工具 | 调用开始（只有输入） | 调用结束且成功（解析 `Output["output"]`） |
| --- | --- | --- |
| `subagent_run` | `verb:"run"`，1 个任务：标题、类型，`status:"waiting"` | `status:"ok"`→`done`，`key`=`task_id`；`status:"skipped"`→`skipped`，`error` 原文 |
| `subagent_fanout` | `verb:"run"`，`tasks[i]` 各一个，`waiting` | `results[i]`：`ok`→`done`；`error` 以 `skipped` 开头→`skipped`；否则 `failed`；`error` 原文（结果里没有 task id，`key` 留空，由生命周期事件绑定） |
| `subagent_send` | `verb:"send"`，1 个任务，`waiting` | `key`=`task_id`，`status`=`running`；`skipped` 同上 |
| `subagent_continue` | `verb:"continue"`，1 个任务，`key`=输入 `task_id` | 用 `record` 填 `key`/标题/类型/状态/`error`/时间 |
| `subagent_status` | `verb:"status"`，1 个任务，`key`=输入 `task_id` | 结果本身就是记录，同上 |
| `subagent_wait` | `verb:"wait"`，同上 | 用 `record`；`timed_out:true` 时 `TimedOut=true` |
| `subagent_close` | `verb:"close"`，同上 | `StopRequested=true`；有 `record` 就用它填，没有就只保留输入的 `key` |
| `subagent_list` | `verb:"list"`，无任务 | `records[i]` 各一个，`Index=i` |

记录状态的映射：`running`→`running`，`ok`→`done`，`failed`→`failed`，`cancelled`→`cancelled`。`ExecutionID`/`StartedAt`/`FinishedAt`：`subagent_run` 取结果的 `run_id`（即第一次执行的 id）和 `finished_at`（`started_at` 结果里没有，留 0，由界面用生命周期事件的时间）；`subagent_send` 取 `run_id`、`started_at`；记录类（continue/status/wait/close/list）取记录的 `execution_id`、`started_at`、`finished_at`；`subagent_fanout` 的结果没有这些，全部留空（界面按生命周期事件计时）。只按 `task_id` 或 `run_id` 查找时输入里没有 `task_id`，开始时任务的 `key` 留空。

调用出错（`evt.Error` 非空）：任务按"开始"那列给出，没有自己结果的任务一律 `status:"failed"`、`error` 留空——调用本身的错误由界面在卡片头下面显示一次，不复制到每一行。

解析失败（结果不是这些形状）是外部输入（模型看到的工具结果文本），按"调用开始"那列给出任务、状态留空，不报错；这是唯一允许的兜底，注释写明原因。

### 3. 文字正文、调用标签、摘要（`pkg/tool/format.go`）

- `FormatToolStepResult`：八个 `subagent_*` 都改为调用一个新函数 `formatSubagentCallStep(evt, maxBytes)`，它**只**从 `SubagentCallFromStep` 的事实生成文字，每个任务一行：`<图标> <标题> · <类型> · <状态短语>`，已结束且 `StartedAt`、`FinishedAt` 都有时再加 ` · <耗时>`（`FinishedAt - StartedAt`，格式与 TUI 已结束工具卡片的时长相同）（图标：`done` ✓、`failed` ✗、`cancelled` ×、`skipped` !，其余无图标；状态短语见下表）。失败/跳过的原因另起一行、缩进两个空格。没有任务时返回空串（界面统一显示 `(No output)`）。删除 `formatFanoutStep`、`formatSubagentRunStep`；`tasksFromInput`、`SubagentTask` 若只剩推导函数在用就并进去，没人用就删。**正文里不得出现 prompt、subagent 的输出和任何 JSON。**

  | 事实 | 状态短语 |
  | --- | --- |
  | `waiting` | `waiting` |
  | `running` | `running` |
  | `running` + `TimedOut` | `still running when the wait ended` |
  | `StopRequested` | `stop requested` |
  | `done` | `done` |
  | `failed` | `failed` |
  | `cancelled` | `cancelled` |
  | `skipped` | `skipped` |

- 调用标签（`toolInvocationNamedLabel` 里加八个 case，删除 `fanoutInvocationLabel`、`subagentRunLabel`）：`subagent_run`→`run <标题>`；`subagent_fanout`→`run <N> <类型> tasks`（全部同类型时带类型，N 为 1 时用 `task`）；`subagent_send`→`send <标题>`；`subagent_continue`→`continue <标题，没有就 task_id>`；`subagent_status`→`check <同上>`；`subagent_wait`→`wait for <同上>`；`subagent_close`→`stop <同上>`；`subagent_list`→`list tasks`。**不截断**（标题本身最长 160 字节）。continue/status/wait/close 在开始时只有 `task_id`，用它；结束后标签用记录里的标题。
- `SummarizeToolStep`：八个工具不走 `"running " + invocation`/`"ran " + invocation`，改为与 TUI 卡片头同义的小写句子，例如 `running 3 general-purpose tasks`、`ran 1 general-purpose task`、`sent 1 general-purpose task`、`continued 计划001 Go车道实施`、`checked 计划001 Go车道实施`、`waited for 计划001 Go车道实施`、`stopped 计划001 Go车道实施`、`listed 3 tasks`、出错时沿用 `failed: <错误>`。具体措辞以计划 013 的卡片头表为准（同一张表的小写形式），两处在测试里交叉断言。

### 4. 历史行的事实（`pkg/turn/compaction.go`）

```go
// SubagentCallsInTranscript derives the card facts of every subagent_* call
// a stored transcript answered, keyed by tool call id, from the call's
// arguments and the result the model received — the same derivation the live
// path applies, so a reloaded card says what the live one said, including in
// sessions recorded before these facts existed.
func SubagentCallsInTranscript(turns []state.Message) map[string]event.SubagentCall
```

实现：遍历 assistant 行的 `state.ParseMessageParts(…)` 取工具调用（id、名字、参数 JSON），遍历 tool 行用 `ParseMessageParts` 拿到它回答的调用 id，`tool.SubagentCallFromStep` 喂 `Kind: StepKindToolCompleted`、`Input`=参数、`Output`=`{"output": 行的 content}`；行是失败的（该行 `tool_display` 部分的 `tool_meta_json` 里 `status` 为 `failed`），把 `content` 作为 `Error` 传入。本计划只提供函数和测试；TUI 回放（计划 013）和网页历史接口（计划 014）各自接入。

### 5. 引擎修正（`pkg/run/subagent.go`）

- A1：`subagent_send` 构造 `baseCtx` 时加上 `baseCtx = tool.WithToolUseID(baseCtx, tool.ToolUseIDFromContext(ctx))`，注释写明"派发它的调用 id 是界面把它画进那张卡片的唯一依据；异步执行脱离了调用的上下文，所以要显式带过去"。
- A2：`continueSubagentExecution` 里 `lifecycle` 和 `final` 在改 `ParentToolCallID` 的同时把 `TaskIndex` 置 0，注释写明"`ParentToolCallID` 和 `TaskIndex` 一起说的是'哪次调用的第几个任务'；continue 调用只有一个任务"。
- 两处都不改返回给模型的任何文字。
- 结束时间（为计划 013/014 的任务计时）：`pkg/event/run_events.go` 的 `SubagentEndedPayload` 加 `FinishedAtMs int64 \`json:"finished_at_ms,omitempty"\``，注释写明"the moment this execution actually stopped; a reaped execution's is when its process was last seen, not when it was reaped"。`notifySubagentEndedDirect` 填 `time.Now().UnixMilli()`。SRT-004 的回收器发同一事件时填运行被盖章的 `finished_at_ms`（已在 SRT-004 第 2 节补了这一句）；它若先于本计划实施，本计划实施时核对那边确实填了。

### 6. 旧数据迁移（`pkg/state/schema_migrations.go`，D10 选 A 时才做）

新增下一个版本号的迁移（`bda9505` 时迁移表已到 V6，下一个是 `migrateStateV6ToV7`；执行时以 `stateSchemaMigrations` 里最大的版本 +1 为准，同时改 `stateSchemaVersion`），**只改数据、不改结构**：

```sql
UPDATE fb_session_events
SET payload_json = json_set(payload_json, '$.parent_tool_call_id', (
  SELECT json_extract(s.payload_json, '$.step_id') FROM fb_session_events s
  WHERE s.session_id = fb_session_events.session_id
    AND s.event_type = 'tool_call_completed'
    AND json_valid(s.payload_json)
    AND json_extract(s.payload_json, '$.tool_name') = 'subagent_send'
    AND json_valid(json_extract(s.payload_json, '$.output.output'))
    AND json_extract(json_extract(s.payload_json, '$.output.output'), '$.run_id')
        = json_extract(fb_session_events.payload_json, '$.execution_id')
  LIMIT 1))
WHERE event_type IN ('subagent_spawned', 'subagent_ended')
  AND json_valid(payload_json)
  AND IFNULL(json_extract(payload_json, '$.parent_tool_call_id'), '') = ''
  AND EXISTS (<同一个子查询>);
```

执行者先把这段 SQL 在本机状态库的**只读副本**上跑一遍 SELECT 版本（把 `UPDATE … SET` 换成 `SELECT event_type, json_extract(payload_json,'$.title'), <子查询>`），确认命中的就是 `subagent_send` 派发的事件、goal check 不命中。`schema.sql` 不变（结构没变），但版本号随迁移表走；`TestStateMigrationMatchesFreshSchema` 一类测试按需要更新期望的版本号。

## 缓存影响

无，执行者要留证据：

- 不改任何工具的名字、描述、参数 schema（工具表在前缀里）。`git diff` 里 `pkg/run/subagent.go` 的 `jsonschema:` 标签、`addSubagentLifecycleTool` 的描述字符串、`RegisterToolMeta` 的 `Description` 都不得变化。
- 不改任何工具返回给模型的字符串。第 6 步加一个测试，对 `subagent_send`/`subagent_continue` 的返回值做字段集合断言（与改动前相同）。
- `ToolMeta.SubagentCall` 只进入 `tool_display` 部分和事件；确认它不进模型请求：`grep -n "PartTypeToolDisplay" pkg/state/message_parts.go` 找到 `ParseMessage` 重建模型消息时跳过 `tool_display` 的那段并在报告里引用；如果不跳过，STOP。
- 迁移只改 `fb_session_events`（界面事件），不碰 `fb_messages` 的 `content`/`parts`。

## 范围

**只改这些文件：**
- `pkg/event/tool_events.go`、`pkg/event/tool_events_test.go`、`pkg/event/run_events.go`（只加 `SubagentEndedPayload.FinishedAtMs`）
- `pkg/tool/format.go`、`pkg/tool/notification.go`、`pkg/tool/format_test.go`、`pkg/tool/notification_test.go`
- `pkg/turn/compaction.go`、`pkg/turn/compaction_test.go`
- `pkg/run/subagent.go`、`pkg/run/subagent_test.go`
- `pkg/state/schema_migrations.go`、`pkg/state/schema_migrations_test.go`（D10 选 A 时）
- 前端共享的事件 fixture（用 `grep -rn "schema_version" frontend/src/lib` 找到；如果事件 fixture 有版本化的 Go/TS 对照测试，在 fixture 里给一个工具事件加上 `subagent_call` 例子）

**不要动：**
- `pkg/tui/**`、`pkg/gateway/**`、`frontend/src/components/**`、`frontend/src/views/**`、`frontend/src/composables/**`——界面接入是计划 013、014 的事。本计划结束时两个界面的样子**不会变好**（TUI 的 fanout 卡片照旧，生命周期工具的卡片输出区变成第 3 节的文字而不是 JSON），这是预期的中间状态。
- 各工具返回给模型的 JSON（见"缓存影响"）。
- `pkg/agent/**`。

## 步骤

### 第 1 步：先写失败的测试

- `pkg/tool/format_test.go` 新增 `TestSubagentLifecycleToolsNeverShowJSON`：对 `subagent_send`、`subagent_status`、`subagent_wait`、`subagent_continue`、`subagent_close`、`subagent_list` 各造一个完成的 `StepEvent`（`Output: {"output": <上面"结果形状"里的 JSON 字符串>}`，其中 `task` 字段放一段含 `SECRET-PROMPT-TEXT` 的 prompt），断言 `FormatToolStepResult` 的结果不含 `{`、`"agent_id"`、`SECRET-PROMPT-TEXT`；断言 `SummarizeToolStep` 和 `BuildToolMeta(evt).Invocation` 不含 `{` 和 `SECRET-PROMPT-TEXT`。对 `subagent_run`、`subagent_fanout` 断言正文不含 `SECRET-PROMPT-TEXT`。
- `pkg/run/subagent_test.go` 新增 `TestSubagentSendLifecycleNamesItsDispatchingCall`：用该文件里现有的 subagent 测试替身（照 `grep -n "func Test.*Send\|spawnAsyncSubagent" pkg/run/subagent_test.go` 找最接近的用例当模板），在带 `tool.WithToolUseID(ctx, "call-send-1")` 的上下文里调用 `subagent_send`，断言发布的 `subagent_spawned` 事件 `ParentToolCallID == "call-send-1"`、`TaskIndex == 0`。再加 `TestSubagentContinueLifecycleIndexIsItsOwnCall`：对一个 `TaskIndex=2` 的记录调 `subagent_continue`（`WithToolUseID(ctx, "call-cont-1")`），断言 spawned/ended 事件 `ParentToolCallID == "call-cont-1"` 且 `TaskIndex == 0`。

**验证**：`CGO_ENABLED=1 go test -tags fts5 ./pkg/tool ./pkg/run -run 'TestSubagentLifecycleToolsNeverShowJSON|TestSubagentSendLifecycleNamesItsDispatchingCall|TestSubagentContinueLifecycleIndexIsItsOwnCall' -count=1` → 三条都失败（第一条因为正文含 `{`；后两条因为 `ParentToolCallID` 为空 / `TaskIndex` 为 2）。任何一条不失败就 STOP（说明现状与本计划描述不符）。

### 第 2 步：引擎修正（A1、A2）与结束时间

按"设计"第 5 节。

同时加 `TestSubagentEndedCarriesItsFinishTime`：一次 `subagent_run` 结束后发布的 `subagent_ended` 的 `FinishedAtMs` 非零，且不早于对应 spawned 事件的时间。

**验证**：`CGO_ENABLED=1 go test -tags fts5 ./pkg/run -count=1` → `ok`（第 1 步后两条与新加的一条都通过）。

### 第 3 步：数据结构与推导函数

按"设计"第 1、2 节。`pkg/tool/format_test.go` 加表驱动测试 `TestSubagentCallFromStep`，覆盖第 2 节表格的每一格（八个工具 × 开始/成功），每格同时断言 `ExecutionID`/`StartedAt`/`FinishedAt` 按第 2 节取到；外加：调用出错、`subagent_fanout` 的 `skipped: empty prompt` 和 `skipped due to fail_fast`、`subagent_wait` 超时、`subagent_close` 无 `record`、`subagent_list` 空列表、fork（类型为空 → `"fork"`）、无 title 的派发（标题取 prompt 第一行）、结果不是预期形状。`pkg/tool/notification_test.go` 加一条：`RunEventFromStep` 产出的完成事件载荷里 `tool_meta.subagent_call` 与 `BuildToolMeta` 的一致。

**验证**：`CGO_ENABLED=1 go test -tags fts5 ./pkg/tool ./pkg/event -count=1` → `ok`。

### 第 4 步：文字正文、调用标签、摘要（A3、A4、A6、A7）

按"设计"第 3 节。改掉断言旧文字（`fanout 4 explore tasks`、`subagent (explore) "…"`、`N tasks (type):`、`subagent type: …`）的既有测试，报告里逐条列出改了哪些、改成什么。

**验证**：
- `CGO_ENABLED=1 go test -tags fts5 ./pkg/tool -count=1` → `ok`，第 1 步第一条转绿。
- `grep -n "func formatFanoutStep\|func formatSubagentRunStep\|func fanoutInvocationLabel\|func subagentRunLabel" pkg/tool/format.go` → 无输出。

### 第 5 步：历史行的事实

按"设计"第 4 节。`pkg/turn/compaction_test.go` 照 `TestRunWorkedLinesCloseEachRunAfterItsLastRow`（`:219`）的写法造 `[]state.Message`（assistant 行带 `tool_calls` 部分，tool 行带回答的调用 id，参照该文件已有的构造辅助函数），新增 `TestSubagentCallsInTranscriptMatchesTheLivePath`：同一组调用，`SubagentCallsInTranscript` 的结果与直接对完成的 `StepEvent` 调 `tool.SubagentCallFromStep` 的结果 `reflect.DeepEqual`；再加一条失败行的用例。

**验证**：`CGO_ENABLED=1 go test -tags fts5 ./pkg/turn -count=1` → `ok`。

### 第 6 步：模型看到的结果没变

`pkg/run/subagent_test.go` 加 `TestSubagentToolResultsKeepTheirShape`：`subagent_send` 返回值解析后的键集合等于 `{agent_id, task_id, run_id, parent_run_id, session_id, worker_session_id, query_source, status, started_at, agent_kind, agent_type, runtime_kind}`；`subagent_continue` 返回 `{record}`。

**验证**：`CGO_ENABLED=1 go test -tags fts5 ./pkg/run -run TestSubagentToolResultsKeepTheirShape -count=1` → `ok`。

### 第 7 步：旧数据迁移（D10 选 A 时）

按"设计"第 6 节。`pkg/state/schema_migrations_test.go` 照 `TestStateV3UpgradesToV4WithSessionsIntact`（`:1302`）新增 `TestStateUpgradeBackfillsTheSendCallOfLegacySubagentEvents`：在上一版本的库里插入一条 `tool_name=subagent_send` 的 `tool_call_completed` 事件（`step_id="call-send-1"`，`output.output` 里 `run_id="exec-1"`）、一对 `execution_id="exec-1"` 且没有 `parent_tool_call_id` 的 spawned/ended 事件、一对 `execution_id="exec-goal"` 的 goal check 事件；升级后断言前一对的 `parent_tool_call_id == "call-send-1"`，goal check 那对仍为空，其余字段逐字节不变。

**验证**：`CGO_ENABLED=1 go test -tags fts5 ./pkg/state -count=1` → `ok`。另外在 scratch 目录里复制一份本机状态库（`cp ~/.forebrain/state/forebrain.state.sqlite* "$SCRATCH/"`，不要动原库），用本分支构建的二进制或测试辅助函数打开它完成迁移，再跑"设计"第 6 节的 SELECT：`M1`–`M8`、`计划001` 的事件都有了 `parent_tool_call_id`；把查询结果贴进报告。磁盘不足 2 GiB 时跳过这一小步并在报告里注明。

### 第 8 步：全量回归

**验证**：
- `gofmt -l pkg cmd` → 无输出；`go vet -tags fts5 ./...` → 退出码 0
- `CGO_ENABLED=1 go test -tags fts5 ./... -count=1` → 全部 `ok`（TUI、gateway 里断言旧文字的测试如果失败，**只**在它们断言的是 `display_body`/`summary` 文字时更新断言，并在报告里列出；断言卡片样子的留给 013/014，不要在本计划里改 `pkg/tui`、`pkg/gateway` 的产品代码）
- `CGO_ENABLED=1 go test -tags fts5 ./pkg/architecture/ -count=1` → `ok`；`scripts/package-graph.sh && git diff --exit-code pkg/architecture/testdata/graph.json` → 退出码 0
- 死代码与开工前基线一致

## 测试计划

新增：`TestSubagentLifecycleToolsNeverShowJSON`、`TestSubagentCallFromStep`（表驱动）、`RunEventFromStep` 的 `subagent_call` 一致性、`TestSubagentSendLifecycleNamesItsDispatchingCall`、`TestSubagentContinueLifecycleIndexIsItsOwnCall`、`TestSubagentToolResultsKeepTheirShape`、`TestSubagentCallsInTranscriptMatchesTheLivePath`、`TestStateUpgradeBackfillsTheSendCallOfLegacySubagentEvents`。格式参照各文件里紧邻的既有测试。

## 完成标准

- [ ] 第 1–8 步的验证全部通过
- [ ] `grep -rn "Generic fallback" pkg/tool/format.go` 仍在，但八个 `subagent_*` 都有自己的 `toolInvocationNamedLabel` case（`grep -c '"subagent_' pkg/tool/format.go` 的结果在报告里给出并解释）
- [ ] 工具描述、参数 schema、返回给模型的字段没有任何变化（`git diff -U0 pkg/run/subagent.go | grep -n 'jsonschema\|Description\|"Use when'` → 无输出）
- [ ] 报告里引用了 `tool_display` 不进模型请求的代码位置
- [ ] README 状态行已更新

## STOP 条件

- 第 1 步的测试没有失败。
- 计划 010 未完成，或 `tool.SubagentTaskTitle` 不存在。
- 发现 `tool_display` 部分会进入模型请求。
- 迁移的 SELECT 在本机副本上命中了非 `subagent_send` 派发的事件，或同一事件命中多个调用。
- 需要新建生产文件或新增包间 import 才能完成。
- `stateSchemaMigrations` 里已经有别的计划占用了下一个版本号且它的迁移还没合入——不要抢号，STOP 报告。

## 维护说明

- 以后新增 `subagent_*` 工具，必须同时在 `SubagentCallFromStep`、`toolInvocationNamedLabel`、`SummarizeToolStep` 和 `formatSubagentCallStep` 里给它一个分支；`TestSubagentLifecycleToolsNeverShowJSON` 的工具列表要加上它。评审时看 `grep -n '"subagent_' pkg/tool/format.go` 的数量是否跟上。
- 改任何 `subagent_*` 工具返回给模型的 JSON 形状时，`SubagentCallFromStep` 的解析和 `TestSubagentCallFromStep` 要一起改；那样的改动还会改模型看到的工具结果，按缓存铁律评估。
- 计划 005 会给 `subagent_spawned` 加 `Origin`；用户在 subagent 视图里发起的执行没有派发调用，`ParentToolCallID` 为空是对的，界面按计划 013/014 的"独立卡片"规则处理，不要再用迁移去补它们。
