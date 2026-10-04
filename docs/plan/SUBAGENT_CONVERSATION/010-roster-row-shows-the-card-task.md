# 计划 010：roster 的 subagent 行显示的任务就是卡片上的任务

> **执行者须知**：逐步执行。每一步都运行"验证"命令并确认结果后再进入下一步。出现"STOP 条件"里的任何情况，立即停止并报告。
> 完成后更新 `docs/plan/SUBAGENT_CONVERSATION/README.md` 里本计划的状态行。**不要提交代码。** 先读 README 的"全局规则"。
>
> **漂移检查（先运行）**：
> `git diff --stat 1a6d708 -- pkg/agent/subagent_history.go pkg/run/subagent.go pkg/tui/render.go pkg/tui/reducer.go pkg/tui/notify.go pkg/tui/chat_slash.go frontend/src/components/chat/AgentViewTabs.vue`
> 有不属于本计划的既有改动时，按函数名和注释原文核对"现状"摘录；对不上就 STOP。如果计划 001 已经做完，`render.go`/`reducer.go` 的 roster 光标部分会不同，那是预期的，本计划不碰光标。

## 状态

- **优先级**：P1（owner 2026-10-04 追加的第 6 点）
- **工作量**：S
- **风险**：LOW
- **依赖**：无（与 001 同属 roster，建议紧接 001 执行）
- **类别**：bug
- **基线**：提交 `1a6d708`，2026-10-04

## owner 的要求（逐字）

> 如forebrain harness tui的截图所示，agent roster 里的subagent行的任务与subagent消息卡片的任务不一致，请定位根因并修复bug，加入本期计划。agent roster 里的subagent行的任务必须使用subagent消息卡片的任务

截图：主视图的卡片 `Running 3 general-purpose tasks…` 下列着 `任务16 migrate 导入`、`任务17 扩展目录`；roster 里对应的两行却是 `general-purpose · 8c5c  edit /Users/doudou/workspace/…`、`general-purpose · 37aa  read /Users/doudou/workspace/…`。

## 为什么要做（根因）

两层原因：

1. **直接原因：roster 行优先显示"正在跑的工具"。** `pkg/tui/render.go` 的 `agentRosterRowDetail` 按 `row.Activity` → `row.Title` → `row.Task` 的顺序取第一个非空值，而 `row.Activity` 在 subagent 每调一次工具时被改写成那次工具的标签（`pkg/tui/reducer.go` 的 `updateSubagentActivity`，调用点在工具消息处理里：`r.updateSubagentActivity(agentID, fanoutToolLabel(msg))`，以及 `ActivityStatusUpdatedMsg`）。所以 subagent 只要在跑工具，roster 行就显示 `edit /…`、`read /…`，不显示任务。这是当初有意的设计（函数注释写着"the tool it is running right now when one is in flight"），owner 现在明确否定它：roster 行必须显示卡片上的任务。正在跑的工具在卡片的第三层（`└ read …`、`… +48 tool uses`）已经有了，roster 不需要再说一遍。
2. **深一层：卡片和 roster 各自推导"任务名"。**
   - 卡片：`pkg/tui/reducer.go` `parseFanoutTasksFromMeta` 从工具调用的输入里取 `title`，为空时用 `truncateForDisplay(prompt, 80)`；绘制时再 `singleDisplayLine`（把多行用空格接成一行）。
   - roster：行的 `Title` 来自 `SubagentSpawnedMsg.Title`，即引擎的 `entry.Title = subagentDisplayTitle(d.title, taskText)`（`pkg/run/subagent.go` 约 `:1745`）：取 title，为空时取 prompt，**只留第一行**，截到 160 字节。
   - 两条规则在"派发时没给 title"或"title 有多行"时给出不同的文字。网页的卡片（`SubagentCard.vue` 用 `card.title || card.task`）和标签页（`AgentViewTabs.vue` 只显示类型，悬停提示用 `entry.task` 即整段 prompt）又是另外的说法。

修复：任务名只有一个推导函数，放在 `pkg/agent`（引擎、TUI、gateway 都 import 它）；引擎写进记录的标题、TUI 卡片上的任务、roster 行、网页卡片和标签页全部用这一个结果。roster 行只显示它。

## 现状（2026-10-04 工作区的事实）

- `pkg/tui/render.go`：

  ```go
  func agentRosterRowDetail(row AgentRosterRow) string {
  	detail := strings.TrimSpace(row.Activity)
  	if detail == "" {
  		detail = strings.TrimSpace(row.Title)
  	}
  	if detail == "" {
  		detail = strings.TrimSpace(row.Task)
  	}
  	...
  	return truncateForDisplay(detail, agentRosterDetailMaxWidth)
  }
  ```

  `agentRosterDetailMaxWidth = 56`。

- `pkg/tui/notify.go` `AgentRosterRow` 有字段 `Activity string // current tool call description for fanout third layer`。读它的只有 `agentRosterRowDetail`（`grep -rn "\.Activity\b" pkg/tui/*.go` 核实；`fs.Tasks[i].Activity`、`t.Activity` 是卡片任务 `FanoutTaskState` 的同名字段，不是它）。
- `pkg/tui/reducer.go`：`upsertSubagentRoster` 在状态不再是 running 时清 `row.Activity`；`updateSubagentActivity` 写它；两个调用点见上。
- `pkg/run/subagent.go`：`subagentTitleMaxBytes = 160`；`subagentDisplayTitle(title, task string) string`；`prepareSubagentExecutionResolved` 用它生成 `entry.Title`。
- `pkg/tui/reducer.go` `parseFanoutTasksFromMeta`：单任务（`subagent_run`，输入字段 `task`/`title`）和多任务（`subagent_fanout`，输入 `tasks[].prompt`/`tasks[].title`）两个分支都是 `title`，为空时 `truncateForDisplay(prompt, 80)`。`renderFanoutContent` 绘制时 `title := singleDisplayLine(t.Title)`，为空时 `truncateForDisplay(t.Prompt, 80)`。
- `pkg/tui/chat_slash.go` `AgentRosterSnapshot` 用 `streamSubagentRosterRow(entry)` 把注册表条目变成行，标题取 `entry.Title`。
- 网页：`frontend/src/components/chat/SubagentCard.vue` 的 `detail` 用 `props.card.title || props.card.task`；`frontend/src/components/chat/AgentViewTabs.vue` 的标签文字是 `entry.agentType || t('agents.subagent')`，`:title="entry.task || entry.agentId"`。

## 设计

1. **一个推导函数。** 在 `pkg/agent/subagent_history.go`（`RosterKey` 旁边）新增：

   ```go
   // TaskTitle is the one name a dispatched task goes by everywhere it is
   // shown — its card, its roster row, its tab: the title the dispatching
   // agent gave it, or the first line of its prompt when it gave none, kept
   // to TaskTitleMaxBytes. Every surface derives the name here, so no two
   // places can call one task by two names.
   func TaskTitle(title, prompt string) string
   ```

   规则沿用引擎今天的 `subagentDisplayTitle`（它是落库、重放的那一份）：title 去首尾空白，为空用 prompt；只取第一行；按 rune 边界截到 `TaskTitleMaxBytes = 160` 字节（与今天的截断函数行为一致，执行者把 `truncatePreviewText` 的实现读一遍，确认不会截断在 rune 中间；如果会，按 rune 截，这是顺带修正，在报告里说明）。
2. **引擎用它。** 删除 `pkg/run/subagent.go` 的 `subagentDisplayTitle` 和 `subagentTitleMaxBytes`，调用点改为 `agent.TaskTitle(...)`。
3. **TUI 卡片用它。** `parseFanoutTasksFromMeta` 两个分支都改为 `Title: agent.TaskTitle(title, prompt)`。`renderFanoutContent` 里 `title` 为空的回退分支随之删除（`TaskTitle` 只在 title 和 prompt 都为空时返回空，那时卡片本来也没有可显示的任务）。`singleDisplayLine(t.Title)` 保留与否：`TaskTitle` 已是单行，保留它无害但多余，删掉。
4. **roster 行只显示标题。** `agentRosterRowDetail` 改为只取 `row.Title`（为空返回 `""`），截到 `agentRosterDetailMaxWidth`；更新函数注释为"the task this agent was dispatched to do, by the same name its card shows"，删掉关于"正在跑的工具"的说法。删除 `AgentRosterRow.Activity`、`Reducer.updateSubagentActivity` 及其两个调用点和 `upsertSubagentRoster` 里清它的那行。`ActivityStatusUpdatedMsg` 的处理里保留"更新卡片任务的 activity 并重画卡片"的部分。
5. **网页。** 网页卡片读 `card.title`（引擎已用 `TaskTitle` 生成），删掉 `|| props.card.task` 回退（它会让没有标题时卡片显示整段 prompt，和终端不同）。标签页与 TUI 的 roster 行对应：文字改为 `类型 · 标题`（标题用 CSS 截断），`:title` 悬停提示改为完整标题；两个都只读 `entry.title`。需要的文案键已存在则复用，不存在则中英文同时加。

## 缓存影响

无。`TaskTitle` 只影响显示用的标题和记录里的 `title` 字段，不进入任何模型请求（派发给 subagent 的是 prompt 原文，不是标题）。执行者在第 2 步用 `grep -rn "subagentDisplayTitle\|entry.Title" pkg/run` 确认标题没有被拼进任何提示词；如果有，STOP。

## 范围

**只改这些文件：**
- `pkg/agent/subagent_history.go` 及其测试 `pkg/agent/subagent_history_test.go`
- `pkg/run/subagent.go` 及其测试
- `pkg/tui/render.go`、`pkg/tui/reducer.go`、`pkg/tui/notify.go` 及对应测试
- `frontend/src/components/chat/SubagentCard.vue`、`frontend/src/components/chat/AgentViewTabs.vue`，必要时 `frontend/src/locales/index.ts`，以及它们的测试

**不要动：** 卡片第三层的工具进度（`RecentTools`、`… +N tool uses`、`⎿ activity` 行）；roster 的光标逻辑（计划 001）；`pkg/agent` 的其它函数。

## 步骤

### 第 1 步：先写失败的测试

在 `pkg/tui/chat_session_test.go`（紧挨 `TestAgentRosterRowShowsTheTaskTitleNotThePrompt`）加 `TestRosterRowShowsTheCardTaskWhileItsAgentRunsTools`：用 `Reducer` 依次喂 fanout 工具开始消息（两个任务，title 分别为 `任务16 migrate 导入`、`任务17 扩展目录`）、两个 `SubagentSpawnedMsg`（`ParentToolCallID`、`TaskIndex` 对上，`Title` 同上）、各一条该 subagent 的工具消息（`read /a/b.go`、`edit /c/d.go`）。然后用 `formatAgentRosterLines(reducer.AgentRosterSnapshot(), false, -1, 120)` 取 roster 行，断言两行分别含两个任务标题、都不含 `read /` 或 `edit /`；再用 `renderFanoutContent` 取卡片内容，断言卡片上的任务文字与 roster 行里的标题逐字相同。

再加 `TestUntitledTaskHasOneNameOnCardAndRoster`：派发时没有 title、prompt 为两行（`"first line of the job\nsecond line"`），断言卡片任务和 roster 行标题都是 `first line of the job`。

**验证**：`CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -run 'TestRosterRowShowsTheCardTaskWhileItsAgentRunsTools|TestUntitledTaskHasOneNameOnCardAndRoster' -count=1` → 两条都失败（第一条 roster 行含 `read /`；第二条卡片是两行合并或截断后的 prompt）。不失败就 STOP。

### 第 2 步：`agent.TaskTitle`，引擎改用它

按"设计"第 1、2 条。`pkg/agent/subagent_history_test.go` 加表驱动测试 `TestTaskTitle`：有 title、title 前后空白、无 title 用 prompt 第一行、超长按字节截断且不截断 rune、两者皆空返回空。

**验证**：`CGO_ENABLED=1 go test -tags fts5 ./pkg/agent ./pkg/run -count=1` → `ok`；`grep -rn "subagentDisplayTitle\|subagentTitleMaxBytes" pkg` → 无输出。

### 第 3 步：TUI 卡片和 roster 行

按"设计"第 3、4 条。

**验证**：
- `grep -rn "updateSubagentActivity\|row.Activity" pkg/tui` → 无输出；`AgentRosterRow` 不再有 `Activity` 字段。
- `CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -count=1` → `ok`（第 1 步两条转绿；改掉断言了"roster 显示正在跑的工具"的旧测试，把它们改成断言显示任务标题，并在报告里列出改了哪些）。

### 第 4 步：网页

按"设计"第 5 条。给 `AgentViewTabs` 加组件测试（新建 `AgentViewTabs.test.ts`，照 `frontend/src/components/chat/LspRecommendationCard.test.ts` 的写法）：标签文字含类型和标题，悬停提示是完整标题，不是 prompt。`SubagentCard` 若有测试则补"无标题时不显示 prompt"；没有就新建 `SubagentCard.test.ts`。

**验证**：`cd frontend && corepack pnpm test` → 全部通过；`cd frontend && corepack pnpm exec vue-tsc --noEmit -p tsconfig.json` → 退出码 0。

### 第 5 步：真机

TUI：用计划 001 第 5 步的 `tool` 模式脚本，但改成 `subagent_fanout` 两个任务（`{"name":"subagent_fanout","arguments":{"tasks":[{"title":"任务16 migrate 导入","prompt":"run the command","subagent_type":"general-purpose"},{"title":"任务17 扩展目录","prompt":"run the command","subagent_type":"general-purpose"}]}}`，后接两条 `{"name":"shell","arguments":{"command":"sleep 40"}}`），等卡片出现后 `$D screen`。

网页：`scripts/acceptance/web_e2e.sh` 的假模型流程里若已有 subagent 场景，补一条断言"标签页文字含任务标题"；没有就在报告里写明只做了组件测试，网页真机放到计划 009 一并做。

**验证**：TUI 屏幕上 roster 两行分别是 `general-purpose · xxxx  任务16 migrate 导入`、`general-purpose · xxxx  任务17 扩展目录`，subagent 在跑工具时也不变；卡片上的两行任务文字相同。把屏幕输出贴进报告。

## 完成标准

- [ ] 第 1–4 步的测试全部通过；`CGO_ENABLED=1 go test -tags fts5 ./... -count=1` 全部 `ok`
- [ ] `grep -rn "subagentDisplayTitle\|updateSubagentActivity\|row.Activity" pkg` → 无输出
- [ ] 前端单测、类型检查通过
- [ ] 架构测试、死代码检查通过
- [ ] 第 5 步 TUI 屏幕输出已附在报告里
- [ ] README 状态行已更新

## STOP 条件

- 第 1 步的测试没有失败。
- 发现标题被拼进了某个发给模型的提示词（改它会动缓存前缀）。
- 除 `agentRosterRowDetail` 外还有别的地方读 `AgentRosterRow.Activity`（例如网页 roster 接口），删除字段会改变接口形状。

## 维护说明

- 以后任何地方要显示"这个 subagent 在做什么任务"，都调用 `agent.TaskTitle` 或读由它生成的 `title` 字段，不要再写一条自己的回退规则。评审时 grep `truncateForDisplay(.*[Pp]rompt` 之类的写法。
- roster 不再显示实时工具进度。如果以后有人想让 roster 显示进度，先回来问 owner：这次的要求是"roster 行的任务必须使用卡片的任务"。
