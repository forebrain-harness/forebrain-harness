# 202 — 主代理 ⇄ 子代理直接对话与进度查询（全部项目类型，进程内）

- **基线**：`06f1bc57`（dev）+ 当时工作区（187–190 正在工作区里实施，本计划不碰它们的文件）。
- **状态**：TODO
- **优先级 / 工作量 / 风险**：P1 / M / MED
- **依赖**：无。**不依赖** 194–201（跨进程协同）；197 之后在本计划交付的同一对工具上扩展跨进程目标。
- **执行隔离**：in-tree，**零 commit**（owner 手动提交）。

> **执行者须知**：逐步执行，每步跑「验证」并确认结果后再进下一步。碰到 §8 STOP 条件就停下报告，不要自行发挥。完成后把 `plans/README.md` 里 202 那一行的状态改为 DONE 并写一句结果。
>
> **Drift check（先跑）**：`git diff --stat 06f1bc57 -- pkg/run/subagent.go pkg/run/turn_input.go pkg/run/controller.go pkg/run/typedsubagent_llm.go pkg/tool/state.go pkg/agent/subagent_registry.go pkg/state/todo.go`。有输出就逐条对照 §2 的摘录；对不上就 STOP。

## 0. 为什么做、owner 裁决（执行期不得改）

今天主代理派出子代理之后，只能等它跑完：`subagent_status` / `subagent_wait` / `subagent_list` 只回一条任务记录（状态、任务、最终输出、报错、时间戳、模型），看不到它正在做什么、做了几步、待办完成了多少、最新说了什么；主代理也没有任何工具能给运行中的子代理发话，子代理也无法主动找主代理。owner 2026-10-31 裁定：

1. **主代理必须能主动询问子代理的执行进度与执行情况**——没有就新增。
2. **所有类型的项目（办公创作、创意编程、后台跑批）都提供主代理与子代理直接对话的工具**；跑批里同样支持派发、插话、发消息、全部子代理工具。
3. **工具名沿用 `peer_list` / `peer_send`**（与跨进程协同同一套词汇）。
4. **不受任何开关约束**：不读任何 `peers` 配置，不看 `peer.Enabled()`，任何平台都有。
5. 「跑批不收输入」只拦**人**（终端 / 网页）和**其它进程**，不拦本会话自己的代理之间。
6. 沿用既有规则：**空闲的子代理不收消息**，由派发它的代理决定下一步（`pkg/run/subagent.go:2133-2136` `ErrSubagentNotRunning` 的注释，owner 2026-10-26）——主代理给空闲子代理发消息时，提示改用 `subagent_continue`。
7. 沿用既有规则：**子代理只有一层**（子代理不能再派子代理）。发消息不是派发，`peer_*` **不要**加进 `subagent_*` 名单或嵌套派发守卫。

## 1. 交付

- **进度**：`subagent_status`、`subagent_wait`、`subagent_list` 的返回加实时 `progress`（§3.1）。
- **对话**：两个新工具 `peer_list`、`peer_send`（§3.2）——主代理 → 运行中的子代理＝插话；子代理 → 主代理＝插话到主会话；主代理正卡在 `subagent_wait` 时，子代理来信让 wait 立即返回。
- **跑批**：上面全部在跑批会话里可用；人从子代理视图给跑批子代理发消息仍拒绝（不变）。

## 2. 现状事实（已对基线核实，逐条摘录）

### 2.1 子代理工具注册与返回值

- 注册入口 `pkg/run/subagent.go:334-348`：

```go
func RegisterSubagentTool(a *agent.Agent, fac Factory) error {
	...
	cat := SubagentCatalogFor(fac.appConfig(), fac.AgentName).ForProjectType(fac.projectType())
	if !appcfg.SubagentsEnabledFor(fac.appConfig(), fac.AgentName) || len(cat.Public()) == 0 {
		return nil
	}
```
  末尾 `return registerSubagentLifecycleTools(a, fac, cat)`（约 2967 行）。跑批项目**不**排除这些工具（`ForProjectType` 只换描述句）。
- 生命周期工具 `registerSubagentLifecycleTools`（约 2974 行起）：`subagent_send`（异步派发）、`subagent_list`（约 3020）、`subagent_status`（约 3044）、`subagent_wait`（约 3063）、`subagent_continue`（约 3100）、`subagent_close`。
- `subagent_status` 的实现（约 3044-3061）：`lookupSubagentRecord(...)` → `json.Marshal(record)`，只回记录。
- `subagent_wait`（约 3063-3096）：

```go
if handle, ok := agent.RegistryFor(fac.subagentScopeRoot()).Get(query); ok && handle != nil {
	waitCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	record, waitErr := handle.Wait(waitCtx)
	resp := map[string]any{"record": record}
	if waitErr != nil { resp["timed_out"] = true }
	...
```
- `agent.Handle.Wait`（`pkg/agent/subagent_registry.go:51-61`）只等 `h.done` 或 `ctx.Done()`。
- 记录结构 `agent.HistoryEntry`（`pkg/agent/subagent_history.go:21-57`）：`TaskID, RunID, SessionID, WorkerSessionID, Title, Task, Status, Output, Error, StartedAt, UpdatedAt, FinishedAt, AgentType, Model…`——**没有任何运行中的进度**。
- 守卫 `guardSubagentTool`（`subagent.go:496-507`）：子代理运行里调用 `subagent_*` 一律拒绝（「only available to the primary agent」）。`peer_*` **不走**这个守卫。

### 2.2 子代理通道与插话

- `subagentChannel`（`subagent.go:573-606`）：`workerSessionID, agentKey, queue *InputQueue, running *subagentExecution, surface …`。
- `subagentChannelFor`（`subagent.go:641-668`）：队列取自 `fac.ownerControl().SessionQueue(workerSessionID)`。
- `subagentChannelForConversation(r *Runner, conversationSessionID, agentKey string)`（`subagent.go:2146-2158`）：按会话 + `TaskID` 查记录再取通道；`agentKey` 就是 `agent.RosterKey(TaskID, AgentType)`＝`TaskID`（`pkg/agent/subagent_history.go:272-277`）。
- **人的入口** `SendToSubagent`（`subagent.go:2211-2239`）：

```go
if r != nil && r.ProjectType() == state.ProjectTypeBatch {
	return "", ErrBatchTakesNoInput
}
ch, record, err := subagentChannelForConversation(r, conversationSessionID, agentKey)
...
ch.setSurface(surface)
ch.mu.Lock(); running := ch.running; ch.mu.Unlock()
if running == nil { return "", ErrSubagentNotRunning }
if ch.queue.Steer(in) { return SubagentDeliverySteered, nil }
return SubagentDeliveryQueued, nil
```
  调用方只有 TUI（`pkg/tui/chat_session.go:1582`）与网关（`pkg/gateway/api_extra.go:2992`）——它是人的入口，**保持原样**（含跑批拒绝与 `setSurface`）。

### 2.3 主会话输入队列

- `Controller.SessionQueue(sessionID)`（`pkg/run/controller.go:273-281`）：会话的输入队列，首次使用时创建。
- `InputQueue.Steer`（`pkg/run/turn_input.go:738-758`）：队列 `closed` 时直接返回 false（「A closed conversation takes nothing」）。
- 跑批执行把主会话队列关闭：`pkg/process/run_executor.go:205-207` `x.env.Control.SessionQueue(sid).Close()`；`InputQueue.Close` 注释（`turn_input.go:600-605`）：跑批「nobody may steer it mid-flight and nobody may queue a message behind it」。

### 2.4 身份（都从 ctx 取）

- 会话：`tool.ConversationSessionIDFromContext(ctx)`；调用代理的会话：`llm.AgentSessionIDFromContext(ctx)`（主代理＝会话 id；子代理＝它的 worker 会话 id）；是否子代理运行：`tool.IsSubagentRunFromContext(ctx)`（`pkg/tool/state.go:512`）。

### 2.5 子代理看到的工具表

- `typedSubagentToolFilterLLM.Execute`（`pkg/run/typedsubagent_llm.go:124-136`）：先 `dropSubagentManagementTools`（去掉 `subagent_*`），再按类型过滤。
- 内置类型的禁用表 `typedSubagentDisallowedTools`（`pkg/tool/permissions.go:769` 起）：只列文件写入与计划模式工具，不含 `peer_*`。
- 自定义子代理的策略 `SubagentToolPolicy.Blocks`（`pkg/tool/state.go:586-600`）：`pick` 模式下**未勾选的工具一律拦截**——会把 `peer_*` 拦掉，须加豁免（§4 步骤 6）。

### 2.6 进度数据从哪来（全部会话数据都在库里）

- 子代理的对话行：`(*state.SessionStore).ListMessagesBefore(ctx, workerSessionID, 0, limit)`（`pkg/state/session_store.go:1687`）返回按时间顺序的 `state.Message`：`Role, Content, ToolStepID, ToolMetaJSON, ExecStartedAtMs, ExecFinishedAtMs, ExecDurationMs, CreatedAt…`（结构在同文件约 657-680 行）。
- 子代理的待办：`state.Load(stateRoot, workerSessionID)`（`pkg/state/todo.go:59`），文件 `<stateRoot>/state/todos/<会话键>.json`；会话键＝调用代理的 `llm.AgentSessionIDFromContext`（`pkg/tool/session_tools.go:108-114`），子代理即 worker 会话 id。`state.Item{Content, Status, Title}`，状态 `pending / in_progress / completed / cancelled`。
- 子代理的用量：`(*state.RunStore).SessionUsageForSession(ctx, workerSessionID)`（`pkg/state/runrt.go:459`）。

### 2.7 架构约束（`pkg/architecture/cache_test.go`）

- 每包生产文件上限 31（`maxProductionFilesPerPackage`，约 1492 行）；`pkg/run` 现 26 个——本计划新增 2 个，合计 28。测试文件数不得多于生产文件数。
- `pkg/run` 的 import 扇出预算 `"run": {current: 14, target: 12}`（约 216 行）——**新代码只能 import `pkg/run` 已经 import 的包**。
- `Runner` 字段基线 28（约 928 行）——**不得给 `Runner` 加字段**。
- 文件名最多两个下划线。
- 模型可见文本规则（仓库 `FOREBRAIN.md`「LLM-facing prompt text must be provider-neutral and byte-stable」）：工具描述是编译期常量、不含模型 / 厂商 / 他人工具品牌名。
- jsonschema 坑：`description=` 里带逗号会被截断——字段描述一律写在 `jsonschema_description:` 标签里（先例 `pkg/tool/artifacts.go`）。

## 3. 设计

### 3.1 进度（`pkg/run/subagent_progress.go`，新）

```go
// subagentProgress is what a subagent is doing and has done, read from its own
// conversation: the steps it took, the step in flight, its todo list, the last
// thing it said, and what it has used. It never interrupts the subagent.
type subagentProgress struct {
	Phase       string                `json:"phase"` // running | waiting_approval | idle | finished
	ElapsedMs   int64                 `json:"elapsed_ms"`
	Steps       int                   `json:"steps"`
	Current     string                `json:"current,omitempty"`
	Recent      []subagentStepSummary `json:"recent,omitempty"` // newest last, at most 5
	Todos       *subagentTodoSummary  `json:"todos,omitempty"`
	LastMessage string                `json:"last_message,omitempty"` // at most 600 runes
	Usage       *subagentUsageSummary `json:"usage,omitempty"`
}
```
- `Phase`：记录状态已终结 → `finished`；通道 `running != nil` 且该 run 有待批的 wait（`fac.Owner.RunRT.GetWaitForRun`，先例见 `subagent.go` 的 `syncGeneralSubagentRunState`）→ `waiting_approval`；`running != nil` → `running`；否则 `idle`。
- `Steps` / `Current` / `Recent`：从 worker 会话的工具调用行算——`ToolStepID != ""` 的行是一步；`ExecStartedAtMs > 0 && ExecFinishedAtMs == 0` 是正在执行的那步。步骤标题用界面卡头**同一份**文字（引擎的卡头分段，见 `pkg/tool/header_parts.go` / `display_header.go`，从 `ToolMetaJSON` 里取）；**若 `ToolMetaJSON` 里取不到卡头文字，STOP 报告**，不要自拼。`Recent` 每项 `{header, outcome: ok|failed|denied, duration_ms}`。
- `Todos`：`{completed, total, items:[{content, status}]}`；没有待办则省略。
- `LastMessage`：最新一条 assistant 行的正文，按 rune 截到 600。
- `Usage`：`{input_tokens, output_tokens}`。
- 读取限额：只读最新 200 行（`ListMessagesBefore(ctx, wsid, 0, 200)`），不翻页。

接到三个工具：
- `subagent_status` → `{"record": …, "progress": …}`。**注意**：今天它直接回记录本身；改成两键对象是返回形状变化，模型侧无兼容负担（每次调用独立），但须同步改测试。
- `subagent_wait` → 原有 `record` / `timed_out` 之外加 `progress`；另见 §3.2 的提前返回。
- `subagent_list` → 每项加精简进度 `{phase, steps, current, todos:{completed,total}}`（不带 `recent` / `last_message`，控制体积）。
- 三个工具描述改为（编译期常量，逐字）：
  - `subagent_status`：`Use when checking on a subagent without interrupting it: returns its status and live progress (current step, recent steps, todos, latest message, token usage) by task_id or run_id. If both are empty, returns the latest scoped subagent.`
  - `subagent_wait`：`Use when blocking until a previously-sent subagent finishes: waits by task_id or run_id. Returns the latest status and progress even when the wait times out, and returns early when one of your subagents sends you a message.`
  - `subagent_list`：`List recent subagents scoped to the current session and parent run when available, each with a short progress summary.`

### 3.2 `peer_list` / `peer_send`（`pkg/run/peer_tools.go`，新）

**注册**：在 `RegisterSubagentTool` 末尾、`registerSubagentLifecycleTools` 之后注册（同一闸门：子代理功能开着、目录非空——没有子代理就没有对象可说话；197 引入跨进程目标后改为无条件注册）。主代理与子代理共用这张工具表：`peer_*` 不以 `subagent_` 开头，`dropSubagentManagementTools` 不会去掉它们。工具元信息：`peer_list` `ReadOnly: true, ConcurrencySafe: true`；`peer_send` `ReadOnly: true`；分类 `"session"`。

**名字（地址）**：子代理的名字＝它的 roster key（即 `TaskID`）；子代理一侧看主代理的名字固定为 `parent`。

**`peer_list`**（无参数）返回 JSON：
- 主代理调用：`{"self":"parent","agents":[{"name":<TaskID>,"kind":<AgentType>,"status":"running|idle|finished","task":<Title 或 Task 前 120 rune>}…]}`——本会话的子代理（从子代理账本按 `SessionID` 取，复用 `subagent_list` 的取法），运行中的排前。
- 子代理调用：`{"self":<自己的 TaskID>,"agents":[{"name":"parent","kind":"primary","status":"running|idle"}]}`（主会话是否有 run 在跑：`Controller` 的会话状态，执行者在 `controller.go` 里找现成查询；没有现成的就用「主会话队列是否 attached」）。

**`peer_send`**（`{to, message}`；字段描述：`to`＝`The agent's name exactly as peer_list printed it`，`message`＝`The message, self-contained: the recipient sees nothing else you wrote`）：
- 送达给模型的文字一律包成 `<agent-message from="<发送方名字>">…</agent-message>`；发送方名字：主代理＝`parent`，子代理＝它的 `TaskID`。
- **主代理 → 子代理**：`ch, _, err := subagentChannelForConversation(runner, conv, to)`；找不到 → 错误 `no agent named "<to>"; call peer_list`；`running == nil` → 结果 `{"status":"idle","hint":"Your idle subagent takes no messages; use subagent_continue to give it new work."}`；运行中 → `ch.queue.Steer(in)`，结果 `{"status":"delivered"}`（`Steer` 返回 false 则 `{"status":"queued"}`）。**不调用 `setSurface`，不判断项目类型**——把 `SendToSubagent` 里「查运行态 + Steer」这段抽成内部函数 `steerRunningSubagent(ch, in) (SubagentDelivery, error)`，`SendToSubagent` 与 `peer_send` 共用；`SendToSubagent` 的跑批拒绝与 `setSurface` 留在它自己身上。
- **子代理 → `parent`**：`q := fac.ownerControl().SessionQueue(conv)`；用新方法 `q.SteerFromAgent(in)`（§4 步骤 3）投递——它与 `Steer` 相同，只是**不受 `closed` 拦截**（跑批里人不能插话，但本会话的代理可以）。结果：主会话有 run 在跑 → `{"status":"delivered"}`；没有 → `{"status":"queued","hint":"Your dispatcher is not running now; it will see this when it next runs."}`（唤醒空闲主代理不在本计划，见 §7）。
- **唤醒 `subagent_wait`**：每个会话一个「来信信号」（包级 `map[conversationID]chan struct{}`，带 mutex；与 `subagentChannels` 同一种包级表的写法）。子代理 → `parent` 投递成功后发信号；`subagent_wait` 在 `handle.Wait` 之外同时 `select` 这个信号，收到即返回 `{"record":…, "progress":…, "message_from_subagent": true}`——消息本身作为插话，在 wait 返回后的下一个工具边界送达主代理。
- 自己发给自己、`to` 为空、`message` 为空 → 错误，不投递。

**工具描述**（编译期常量，逐字；197 扩展跨进程时**原样沿用**，不改字节）：
- `peer_list`：`List the agents you can message with peer_send, with each one's status and task. The name is the address: pass it to peer_send exactly as printed.`
- `peer_send`：`Send a message to an agent listed by peer_list. A running agent receives it at its next tool boundary. Your idle subagent takes no messages: use subagent_continue to give it new work. A reply arrives as a message from that agent.`

### 3.3 跑批

- 子代理工具在跑批里本来就注册（§2.1），不改。
- `peer_send` 不经 `SendToSubagent`，所以人入口的跑批拒绝不影响它；子代理 → 主代理走 `SteerFromAgent`，不受关闭的队列拦截。
- 人从终端 / 网页子代理视图给跑批子代理发消息：仍 `ErrBatchTakesNoInput`（`SendToSubagent` 不变）。

## 4. 步骤

1. **进度读取**：新建 `pkg/run/subagent_progress.go`，实现 §3.1 的 `subagentProgress` 与 `buildSubagentProgress(ctx, fac Factory, record agent.HistoryEntry) subagentProgress`（只读，不改任何状态）。
   验证：`FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go build -tags fts5 ./pkg/run` → 无输出。
2. **接到三个工具 + 改描述**：`subagent_status` / `subagent_wait` / `subagent_list` 按 §3.1 带上进度、换描述常量。
   验证：`FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/run -run 'Subagent' -count=1` → 既有用例中因返回形状变化而红的，逐个改断言（只改形状，不改语义）；其余全绿。
3. **队列的代理通道**：`pkg/run/turn_input.go` 给 `InputQueue` 加 `SteerFromAgent(in Input) bool`——复制 `Steer` 的清洗与入队逻辑，去掉 `closed` 判断；注释写明「人不能加入跑批任务，但本会话的代理之间可以说话」。`Steer` 本身不改。
   验证：`go test -tags fts5 ./pkg/run -run 'InputQueue' -count=1` → 绿（加一例：关闭的队列 `Steer` 返回 false、`SteerFromAgent` 返回 true 且消息进入 steers）。
4. **抽出插话内部函数**：把 `SendToSubagent` 中「读 `running`、`ErrSubagentNotRunning`、`ch.queue.Steer`」抽成 `steerRunningSubagent`；`SendToSubagent` 改为调用它，自身仍保留跑批拒绝与 `setSurface`。
   验证：`go test -tags fts5 ./pkg/run -run 'SendToSubagent|Subagent' -count=1` → 绿；`go test -tags fts5 ./pkg/tui ./pkg/gateway -run 'Subagent' -count=1` → 绿。
5. **两个工具**：新建 `pkg/run/peer_tools.go`，实现 §3.2；在 `RegisterSubagentTool` 末尾注册。输入结构的字段描述放在 `jsonschema_description:` 标签里。
   验证：`go build -tags fts5 ./...` → 无输出。
6. **子代理能看到这两个工具**：`pkg/tool/state.go` 的 `SubagentToolPolicy.Blocks` 在 `subagent_` 前缀判断之后加：`peer_list`、`peer_send` 永不拦截（注释：与派发者说话是结构性的，不是可勾选的能力）。
   验证：`go test -tags fts5 ./pkg/tool -run 'SubagentToolPolicy|Subagent' -count=1` → 绿（加一例：`pick` 模式只勾 `read_file`，`Blocks("peer_send")==false`、`Blocks("write_file")==true`、`Blocks("subagent_run")==true`）。
7. **`subagent_wait` 提前返回**：按 §3.2 加会话来信信号，`subagent_wait` 同时等它。
   验证：见 §5 用例 6。
8. **全量**：`FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./... -count=1` → 全绿；`go vet ./...` → 无输出；`go test ./pkg/architecture -count=1` → 绿（文件数、扇出、Runner 字段都不超）。
9. **真机验收**（fake driver 驱动不了多轮代理对话；按仓库惯例用 glm-5.3、隔离 home、key 从 `.env` 注入，在主树跑；启动与驱动 TUI 用 `run-forebrain` 技能）：
   - 创意编程项目：让主代理用 `subagent_send` 派一个 explore 子代理做较长的阅读任务 → 让主代理「看看它做到哪了」→ 主代理调用 `subagent_status`，结果里有 `progress.steps > 0`、`current` 或 `recent` 非空。
   - 同一会话：让主代理「问问子代理现在的发现」→ 主代理 `peer_send` 给它 → 子代理视图里出现这条来信 → 子代理用 `peer_send` 回 `parent` → 主代理收到并转述。
   - 跑批项目：建一个任务，任务描述要求「派一个子代理做 X，中途问它进度」→ 执行记录里有 `peer_send` 往返；用网页子代理视图手动发消息 → 仍被拒（「batch tasks take no input」）。
   把三段的会话 id 与关键工具结果贴进本计划末尾「验收记录」。

## 5. 测试计划

新增 `pkg/run/subagent_progress_test.go`、`pkg/run/peer_tools_test.go`；结构照 `pkg/run/subagent_test.go` 里构造假子代理记录 / 通道的现有用例（搜 `subagentChannelFor(` 与 `RegistryFor(` 的测试用法）。用例：

1. 进度：worker 会话里放 3 个完成步、1 个进行中步、一个 2/4 的待办、一条 assistant 文本 → `buildSubagentProgress` 的 `Phase=running, Steps=4, Current` 非空、`Recent` 3 项、`Todos{2,4}`、`LastMessage` 截断到 600 rune。
2. 进度：记录已终结 → `Phase=finished`、`ElapsedMs = FinishedAt - StartedAt`。
3. `peer_send` 主 → 运行中子代理：子代理队列收到一条，正文以 `<agent-message from="parent">` 开头；返回 `delivered`；`ch.surface` 未被改写。
4. `peer_send` 主 → 空闲子代理：返回 `status:"idle"` 与提示，不入队。
5. `peer_send` 子 → `parent`，主会话队列已 `Close()`（模拟跑批）：仍送达（`SteerFromAgent`）；同一队列 `Steer` 返回 false。
6. `subagent_wait` 等一个运行中的子代理，另一 goroutine 让它 `peer_send` 给 `parent` → wait 在超时前返回且 `message_from_subagent:true`。
7. 跑批会话：`SendToSubagent`（人入口）仍返回 `ErrBatchTakesNoInput`；同一子代理收主代理 `peer_send` 成功。
8. `peer_list`：主代理看到本会话的子代理（运行中排前）；子代理只看到 `parent`。
9. 工具描述：四段描述与 §3.1 / §3.2 的常量逐字相等（若仓库已有覆盖工具描述的 golden 测试，加进去；否则在本测试里断言）。

验证：`FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/run ./pkg/tool -count=1` → 全绿，含以上 9 例。

## 6. 范围

**只改**：`pkg/run/subagent_progress.go`（新）及测试、`pkg/run/peer_tools.go`（新）及测试、`pkg/run/subagent.go`（三个工具的返回与描述、`steerRunningSubagent` 抽取、`RegisterSubagentTool` 末尾注册、wait 的信号）、`pkg/run/turn_input.go`（`SteerFromAgent`）、`pkg/tool/state.go`（`Blocks` 豁免）、对应既有测试的断言、`plans/README.md` 本行。

**不改**：
- 194–201 的任何文件与跨进程逻辑（`pkg/peer`、`pkg/process/peer*.go` 都不存在也不要建）。
- `SendToSubagent` 的跑批拒绝与 `setSurface`（人入口语义不变）。
- `pkg/tui`、`pkg/gateway`、`frontend`——包括来信在界面上的样子（卡片属于 199/200，网页改动须先出原型）。
- 唤醒空闲主代理（无 run 时子代理来信只排队，见 §7）。
- 子代理之间互发消息（本计划只做主代理 ⇄ 子代理）。
- 187–190 正在改的配置相关文件。

## 7. 完成判据

- [ ] `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./... -count=1` 全绿；`go vet ./...` 无输出。
- [ ] §5 的 9 个用例存在且通过。
- [ ] `grep -n "peer_list\|peer_send" pkg/run/subagent.go | grep -i "subagentToolName\|guardSubagentTool"` 无输出（没被加进 `subagent_*` 名单 / 嵌套守卫）。
- [ ] `grep -rn "ErrBatchTakesNoInput" pkg/run/subagent.go` 仍在 `SendToSubagent` 里（人入口未放开）。
- [ ] `go test ./pkg/architecture -count=1` 绿。
- [ ] §4 步骤 9 的真机验收记录贴在本文件末尾。
- [ ] 除 §6「只改」清单外无文件改动（`git status` 对照；187–190 工作区里原有的改动不算）。

## 8. STOP 条件

- §2 的摘录与代码对不上（漂移）。
- 卡头文字在 `ToolMetaJSON` 里取不到（§3.1），需要自拼标题时。
- 子代理运行时看到的工具表里没有 `peer_*`，且原因不在 §2.5 列出的三处过滤里。
- 来信插话后，终端或网页把 `<agent-message …>` 原样显示给用户（说明需要界面改动——那属于 199/200，须先出原型）——停下报告，不要改界面。
- 实现需要给 `Runner` 加字段、让 `pkg/run` import 新包、或新增第 3 个生产文件。
- 跑批会话里 `SteerFromAgent` 投递后主代理收不到（说明跑批主会话的投递路径与交互会话不同）。
- 同一步验证两次修正后仍失败。

## 9. 维护注意

- **197** 扩展跨进程目标时：沿用本计划的两段工具描述（字节不变，否则提示缓存前缀位移）、名字规则（子代理＝TaskID、派发者＝`parent`）与 `<agent-message>` 包装；改为无条件注册；跨进程目标在跑批会话里拒绝（跑批只和本会话代理说话）。
- **196** 的「唤醒空闲会话」落地后，子代理 → `parent` 在主代理无 run 时改为唤醒，而不是只排队。
- **199 / 200**：来信在界面上的呈现（谁发的、发给谁）由它们出卡片，网页须先出原型。
- 进度只读最新 200 行：子代理极长的执行里 `steps` 是这 200 行内的计数——若将来要精确总步数，给会话加计数列，不要在这里翻页。
- 本计划让 `subagent_status` 的返回从「记录本身」变成 `{record, progress}`；以后加字段只往 `progress` 里加。

## 10. 验收记录（2026-10-31 执行）

**实现**：§4 步骤 1–8 全部完成并逐项验证（`pkg/run`、`pkg/tool` 全绿，`go vet ./...` 无输出，`./pkg/architecture` 绿，§5 九例见下）。步骤 9 的**真机验收未运行**，原因与手工验收清单如下。

**未运行的原因**：真机验收要求 glm-5.3。用 `~/.forebrain/.env` 的 `ZHIPUAI_API_KEY` 直连探测：

```
POST https://open.bigmodel.cn/api/paas/v4/chat/completions  model=glm-5.3
→ HTTP 429  {"error":{"code":"1113","message":"余额不足或无可用资源包,请充值。"}}
```

与 205 验收时同一状态（账户无余额）。按「不伪造任何验收结果」的规则，没有改用其它 provider 顶替，也没有用 fake driver 编一段多轮代理对话当验收。

**待人工执行的真机验收清单**（隔离 home、主树、key 从 `.env` 注入；启动与驱动 TUI 用 `run-forebrain` 技能）：

1. **创意编程项目 · 进度**：让主代理 `subagent_send` 派一个 explore 子代理做较长的阅读任务 → 主代理「看看它做到哪了」→ 主代理调用 `subagent_status`。预期：返回 `{record, progress}`，其中 `progress.steps > 0`，`current` 非空（形如 `Running grep …`）或 `recent` 非空（每项 `header/outcome/duration_ms`，`header` 与界面上该步卡片抬头逐字一致）。记录该会话 id 与 `subagent_status` 的返回。
2. **同一会话 · 双向对话**：主代理「问问子代理现在的发现」→ `peer_send`（`to` = `peer_list` 打印的名字）→ 预期返回 `{"status":"delivered"}`；子代理在自己的工具边界收到 `<agent-message from="parent">…</agent-message>`，用 `peer_send` 回 `parent`（预期 `{"status":"delivered"}`）；主代理若正卡在 `subagent_wait`，该 wait 提前返回且带 `"message_from_subagent": true`。记录两端的工具结果。
3. **跑批项目 · 同上 + 人入口仍拒**：建一个任务，任务描述要求「派一个子代理做 X，中途问它进度」→ 执行记录里出现 `peer_send` 往返（主代理 → 子代理、子代理 → `parent` 各一条，后者在跑批的关闭队列上仍 `delivered`）；同时从终端 / 网页子代理视图手动发消息给该跑批子代理，预期仍被拒 `batch tasks take no input`（`ErrBatchTakesNoInput`）。记录会话 id 与拒绝文案。

**执行期已用引擎级证据覆盖的部分**（不是真机）：§5 的 9 个用例全部通过，其中 3/4/5/7 通过**真实注册的工具表**（`RegisterSubagentTool` → `peer_send`/`peer_list`/`subagent_wait` 的 handler）调用，断言队列里的落地文本与包装、`idle` 文案、关闭队列仍送达、跑批人入口仍 `ErrBatchTakesNoInput`；进度两例用真实 `fb_messages` 行 + 真实 `state.Save` 待办文件，断言卡头文字由 `ToolMetaJSON` 还原（`Read main.go lines 1-40`、`Ran go build ./...`、`Running ls -la`）。

**执行期发现、留给 199/200 的界面后果**：`<agent-message …>` 包装是模型可见文本，而投递路径把插话作为 user 行渲染（`pkg/tui/chat_turn.go` `recordDeliveredSteers` → `input_delivered` 事件 → 界面的用户消息），因此真机上用户会看到 `<agent-message from="…">…</agent-message>` 原文。按 §8 第四条与 §6「不改界面」，本次不动界面，由 199/200 出卡片。

