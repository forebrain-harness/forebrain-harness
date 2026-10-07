# 计划 005：引擎——每个 subagent 一条独享的用户输入通道

> **执行者须知**：逐步执行。每一步都运行"验证"命令并确认结果后再进入下一步。出现"STOP 条件"里的任何情况，立即停止并报告。
> 完成后更新 `docs/plan/SUBAGENT_CONVERSATION/README.md` 里本计划的状态行。**不要提交代码。** 先读 README 的"全局规则"。
>
> **前置检查**：README 里计划 002、003、004 必须都是 `DONE`，否则 STOP。
>
> **漂移检查**：
> `git diff --stat bda9505 -- pkg/run/subagent.go pkg/run/turn_input.go pkg/run/controller.go pkg/process/worker_cli.go pkg/process/one_shot.go pkg/event/run_events.go`
> 计划 002–004 会改这些文件，这是预期的；按函数名核对"现状"。

## 状态

- **优先级**：P1（owner 第 1、2 点）
- **工作量**：L
- **风险**：MED-HIGH（并发：同一个 subagent 可能同时被模型和用户驱动）
- **依赖**：002、003、004
- **类别**：bug / direction
- **基线**：提交 `bda9505`，2026-10-04

## 为什么要做

owner 的第 1 点：subagent 失败后，用户必须能在它的视图里发消息让它继续；今天这条消息会进主 agent 的队列。第 2 点：subagent 视图必须有这个 subagent 独享的、完整的队列语义和"与用户直接对话"的语义。

引擎层面的根因：

- subagent 的执行上下文被刻意剥掉了输入运行时（`prepareSubagentExecutionResolved` 里 `context.WithCancel(WithoutTurnInputRuntime(runCtx))`，`continueSubagentExecution` 里 `WithoutTurnInputRuntime(ctx)`），所以运行中的 subagent 收不到任何 steer。
- 子运行在控制器里只登记了取消函数（`own.Control.Track(childRunID, sid, cancel)`），没有输入队列；而且登记用的是**对话**的会话 id，不是 worker 会话 id。
- 唯一能让 subagent 继续的入口是模型调用 `subagent_continue`，它对一次性类型直接拒绝，也没有"用户发来的消息"这个概念。

本计划只做引擎：给每个 subagent 一条用户输入通道（队列 + 串行执行 + 边界决定 + 事件），两个界面在 007、008 接上。语义完全复用计划 004 统一后的 `run.InputQueue`，所以 subagent 的排队、召回、中断后恢复与主视图是同一份代码。

## owner 已定的语义（README 决策表）

- **D1 Esc**：在 subagent 视图里，撤回窗口内（用户刚发、它还没开始回答）Esc 撤回发给它的消息；有发给它的待投递 steer 时，Esc 中断它并立即发送；其余情况 Esc 返回主视图；Esc 永不直接取消 subagent。本计划提供前两种的引擎入口。
- **D2**：所有类型都能被用户直接对话，包括一次性类型（explore、plan、cavecrew-investigator、cavecrew-reviewer）和内部保留类型（plan-reviewer、goal-evaluator、guardian——`subagent_defs.go` 里 `OneShot: true` 的三种审批/评估类）。`OneShot`/`Continuable` 只约束模型的 `subagent_continue`。
- **D3**：用户驱动的执行结束后，**不**往主会话注入任何东西；结果写进注册表和账本，主 agent 用 `subagent_status`/`subagent_wait`/`subagent_list` 能读到。

## 现状（计划 002–004 完成之后应有的样子）

- `run.InputQueue`（计划 004）：按会话保存在 `Controller` 里；`Attach(rt)`/`Detach()`；`Steer`、`FollowUp`、`Recall`、`Next(Boundary)`、`TakeAll`、`Discard`、`Preview`；`Input.Payload any`；`Boundary` 三种取值。
- `run.SubagentExecutor`（计划 002 加了 `PersistSubagentTurn`）；`RunSubagentExec(ctx, task, superviseExistingRunID, parentRunID, sessionID, workerSessionID, subagentType string) (string, error)` 只收字符串。
- `subagentRecordContext(ctx, fac, record)`（计划 003）：从记录重建 subagent 自己调用所用的上下文。
- `continueSubagentExecution`（计划 002 改过）、`continueForkSubagent`（计划 002 新增）。
- 事件（`pkg/event/run_events.go`）：`SubagentSpawnedPayload`（`AgentID, AgentType, TaskID, Title, Task, WorkerSessionID, ParentRunID, ParentToolCallID, TaskIndex, ExecutionID`）、`SubagentEndedPayload`、`PendingInputUpdatedPayload{PendingSteers, RejectedSteers, QueuedMessages}`、`QueuedInputReleasedPayload`（计划 004 加了 `Next`）。
- 运行时投递钩子：`TurnInputRuntime` 的变更钩子在 `SteerDelivery.Commit` 时以"已投递的条目"调用（`notifyDelivered`）。
- subagent 的流式输出：`subagentRunContext`（`pkg/run/subagent.go:529`）给子运行装一个新的 `llm.StreamSink`，丢弃了 `OnResponseStarted`。

## 设计

### 1. 通道

在 `pkg/run/subagent.go` 新增私有类型和包级注册表（不给 `Runner` 加字段）：

```go
// subagentChannel is the user's side of one subagent's conversation: the queue
// of what the user sent it, the one execution at a time that consumes it, and
// the surface the user is talking to it from.
type subagentChannel struct {
	workerSessionID string
	agentKey        string
	queue           *InputQueue   // Controller's queue for workerSessionID
	exec            chan struct{} // capacity 1: one execution of this subagent at a time
	mu              sync.Mutex
	running         *subagentExecution // nil when idle
	surface         SubagentSurface    // set by the first user call; nil until then
}

type subagentExecution struct {
	executionID string
	byUser      bool               // started by a message the user sent
	responded   bool               // the model has produced output in this execution
	cancel      context.CancelCauseFunc
	boundary    Boundary           // how it is being stopped, if it is
	input       Input              // the message that started it, when byUser
	userRowID   int64              // that message's row in the worker session
}
```

注册表按 `workerSessionID` 键（全局唯一）。`channelFor(fac, record)` 取或建。

### 2. 每次执行都挂上通道

所有 subagent 执行入口——首次派发（`execGeneralSubagent`、`spawnAsyncSubagent`、fanout 的每个任务）、模型的 `subagent_continue`、plan reviewer、goal check、以及本计划新增的用户入口——都走同一个包装：

```go
// runSubagentExecution runs one execution of a subagent through its channel:
// it waits for the subagent to be free, attaches the channel's steer runtime
// for the execution's duration, and settles the channel's queue at the end.
func runSubagentExecution(ctx context.Context, fac Factory, record agent.HistoryEntry, byUser bool, input Input,
	run func(execCtx context.Context) (string, error)) (string, error)
```

- 等 `exec` 信号量（`ctx` 取消则返回）。模型的 `subagent_continue` 撞上用户正在驱动的执行时，就在这里等它结束——不并发写同一个 worker 会话。
- 新建本次的 `TurnInputRuntime`，`queue.Attach(rt)`，把 `ctx` 里的输入运行时换成它（替换掉 `WithoutTurnInputRuntime`：那是为了不继承**父**运行时，现在换成**自己的**，目的不变）。
- 给子运行的流式输出接一个"已开始回答"的信号：`subagentRunContext` 的 `EventStreamSink` 在第一次 delta / reasoning / 工具开始时把 `running.responded` 置为 true（今天丢弃的 `OnResponseStarted` 不能直接复用——它属于父运行；在 `EventStreamSink` 里加一个可选回调参数）。
- 给运行时的投递钩子接上：每次 steer 被 `Commit` 投递，就发布 `subagent_input_delivered` 事件（见第 5 条）。
- `run` 结束后：`queue.Detach()`；按 `boundary`（未被打断就是 `BoundaryCompleted`）调 `queue.Next(boundary)`，把 `send`/`restore` 交给通道的 `surface.OnBoundary`（没有 surface 时两者必然为空——没有用户操作过这个通道就不会有排队输入；若不为空，说明不变式被破坏，STOP）。

`prepareSubagentExecutionResolved` 里 `own.Control.Track(childRunID, sid, cancel)` 改为用 worker 会话 id 登记，并带上通道的队列（计划 004 之后 `Controller` 按会话找队列）。

### 3. 用户入口（两个界面共用）

在 `pkg/run/subagent.go` 加导出的包级函数和类型：

```go
// SubagentSurface is how the surface the user is talking to a subagent from
// takes part in that conversation.
type SubagentSurface struct {
	// Frame is applied to the context of every execution the user starts:
	// the step hook and approval hooks the surface uses for its own turns.
	Frame func(ctx context.Context) context.Context
	// OnBoundary receives, when an execution of the subagent ends, what its
	// queue decided: send is the user's queued input to run next (the surface
	// merges it and calls SendToSubagent), restore is the input to give back
	// to the subagent's composer.
	OnBoundary func(agentKey string, send, restore []Input)
}

type SubagentDelivery string

const (
	// SubagentDeliveryStarted: the subagent was idle; the message started its next execution.
	SubagentDeliveryStarted SubagentDelivery = "started"
	// SubagentDeliverySteered: it is running; the message reaches it at its next tool boundary.
	SubagentDeliverySteered SubagentDelivery = "steered"
	// SubagentDeliveryQueued: it is running and the message waits for the execution after this one.
	SubagentDeliveryQueued SubagentDelivery = "queued"
)

func SendToSubagent(ctx context.Context, r *Runner, surface SubagentSurface, conversationSessionID, agentKey string, in Input, mode TurnInputMode) (SubagentDelivery, error)
func SubagentInputPreview(r *Runner, conversationSessionID, agentKey string) QueuePreview
func RecallSubagentInput(r *Runner, conversationSessionID, agentKey string) (Input, bool)
func InterruptSubagentToSend(r *Runner, conversationSessionID, agentKey string) bool
func WithdrawSubagentInput(r *Runner, conversationSessionID, agentKey string) ([]Input, bool)
func DiscardSubagentInput(r *Runner, conversationSessionID, agentKey string) int
func SubagentRunning(r *Runner, conversationSessionID, agentKey string) bool
```

语义：

- **找记录**：`agent.GetMerged(scopeRoot, agent.Query{SessionID: conversationSessionID, TaskID: agentKey})`；找不到返回 `ErrSubagentNotFound`（新哨兵错误）。按对话过滤，所以别的对话、别的主 agent 的 subagent 找不到（租户边界）。
- **SendToSubagent**：
  - 通道正在执行：`mode == steer` 时 `queue.Steer(in)`，接受 → `Steered`；不接受（例如运行时正处于不收 steer 的阶段）或 `mode == follow_up` → `queue.FollowUp(in)` → `Queued`。
  - 通道空闲：在后台 goroutine 里启动一次用户执行（第 4 条），立即返回 `Started`。
  - 第一次调用时记下 `surface`；之后的调用若传了新的 `surface`（例如界面重连），以新的为准。
- **InterruptSubagentToSend**（D1 第二种）：只在通道正在执行且队列里有未投递 steer 时生效：设 `boundary = BoundaryInterruptToSend` 并取消本次执行，返回 true；否则返回 false（界面据此决定 Esc 是否改为"返回主视图"）。
- **WithdrawSubagentInput**（D1 第一种）：只在本次执行是用户发起的、且 `responded == false` 时生效：设 `boundary = BoundaryInterrupted`，取消执行，把 worker 会话里那条用户消息标为撤回（`SessionStore.WithdrawUserTurn(ctx, workerSessionID, userRowID)`，与主会话撤回用同一个存储方法），并返回"被撤回的消息 + 其后排队的全部条目"（`TakeAll`，按写下的顺序）；否则返回 `nil, false`。撤回的执行**不**发布 `subagent_ended`、不写结果（与主会话撤回"当作没发生过"一致；执行者读 TUI 主视图撤回的处理确认这一点，不一致就 STOP）。
- **Recall / Discard / Preview**：直接委托给通道的 `InputQueue`。
- **SubagentRunning**：通道是否正在执行（计划 003 的 `SubagentCompactTarget` 改用它，替换掉按注册表句柄判断的写法，让"运行中"只有一个定义）。

### 4. 用户发起的执行

```go
func startUserSubagentExecution(ctx context.Context, fac Factory, ch *subagentChannel, record agent.HistoryEntry, in Input)
```

- 上下文：`surface.Frame(context.Background())` → `subagentRecordContext(…, record)`（计划 003）→ 通道的运行时、取消函数。
- **不检查 `OneShot`/`Continuable`**（D2）。
- 输入是用户消息的 parts 原样（可以带图片）。为此把执行器接口的字符串参数换成结构体：

  ```go
  type SubagentExecRequest struct {
  	Task            string            // the text input, as today
  	Parts           []llm.ContentPart // when set, the input as parts (images included); Task is its text
  	SuperviseRunID  string
  	ParentRunID     string
  	SessionID       string
  	WorkerSessionID string
  	SubagentType    string
  }
  RunSubagentExec(ctx context.Context, req SubagentExecRequest) (string, error)
  ```

  `process.RunSubagentSupervised` 把 `Parts` 交给 `run.Options.InputParts`，用户消息落库时用 `turn.PersistUserTurn` 的 `Parts` 字段。所有调用点和测试替身同步改。
- 生命周期事件：开始时发布 `subagent_spawned`（新的 `ExecutionID`，`Task` = 用户消息的显示文本，`Origin` = `"user"`，见第 5 条），结束时 `subagent_ended`；注册表句柄和账本照常更新（`finishSubagentExecution` / `AppendHistory`），`Output` 合并规则沿用 `mergeContinuationOutput`。这就是 D3 里"主 agent 用 `subagent_status` 能读到"的来源。
- 执行体：typed → `executeSubagent`；fork → `continueForkSubagent`（计划 002）；都经过 `runAcrossApprovals`（审批照常弹给用户）和 `runSubagentExecution`（第 2 条）。
- 记录 `userRowID`：`PersistUserTurn` 返回的行 id 要传回通道（`RunSubagentSupervised` 把它放进 `SubagentTurn` 或通过一个回调交回；执行者选一种不增加 `Runner` 字段的方式，并在注释里写明）。

### 5. 事件（`pkg/event/run_events.go`，只改这个已有文件）

- `SubagentSpawnedPayload` 加 `Origin string \`json:"origin,omitempty"\``：`"user"` 表示这次执行由用户的消息开始；空表示由派发它的 agent 开始。
- 新事件类型 `RunEventSubagentInputDelivered = "subagent_input_delivered"`，载荷 `SubagentInputDeliveredPayload{AgentID, ExecutionID, Text string}`：用户发给运行中 subagent 的 steer 在工具边界投递给了模型。界面把它画成该 subagent 视图里的一条用户消息；它会被持久化，重放时同样画出。
- `PendingInputUpdatedPayload` 加 `AgentID string \`json:"agent_id,omitempty"\``：subagent 通道的预览变化时发布（每次 `Steer`/`FollowUp`/`Recall`/投递/`Discard` 之后），空表示主会话。
- `QueuedInputReleasedPayload` 加 `AgentID`（网页用；TUI 走 `OnBoundary` 回调，不依赖这个事件）。
- 所有新字段都是新增的可选字段；旧客户端忽略。Go/TS 共享的事件 fixture（`frontend/src/lib/forebrainGatewayRuntime.ts` 及其测试用的版本化 fixture，执行者用 `grep -rn "schema_version" frontend/src/lib` 找到）同步加例子。

## 缓存影响

- 用户发起的执行：worker 会话的历史 + 新用户消息追加在尾部，前缀与这个 subagent 上一次请求相同（计划 002 已保证），命中率与模型的 `subagent_continue` 相同。
- steer：在工具边界追加到尾部，与主会话 steer 相同。
- 第 4 步的第 6 条测试（`TestUserMessageReusesTheSubagentsPrefix`）用捕获请求的测试 LLM 断言：用户发起的第一次请求 = 该 subagent 上一次请求的全部消息 + 上一次的回答 + 用户消息。

## 范围

**只改这些文件：** `pkg/run/subagent.go`、`pkg/run/turn_input.go`（如需给 `EventStreamSink` 加回调则改 `subagent.go` 里它的定义处）、`pkg/run/controller.go`、`pkg/process/worker_cli.go`、`pkg/process/one_shot.go`、`pkg/event/run_events.go`、`frontend/src/lib/forebrainGatewayRuntime.ts` 的事件类型（只加字段，不改行为）、所有 `SubagentExecutor` 测试替身所在的测试文件，以及上述文件的测试。

**不要动：** `pkg/tui`、`pkg/gateway`、`frontend` 的界面行为（007、008）；`subagent_continue` 对一次性类型的拒绝（它约束的是模型）。

## 步骤

### 第 1 步：执行器接口改为结构体参数

按"设计"第 4 条换掉 `RunSubagentExec` 的参数，所有调用点和替身同步。行为不变。

**验证**：`go build -tags fts5 ./...` 成功；`CGO_ENABLED=1 go test -tags fts5 ./pkg/run ./pkg/process ./pkg/tui -count=1` → `ok`。

### 第 2 步：事件字段

按"设计"第 5 条。

**验证**：`CGO_ENABLED=1 go test -tags fts5 ./pkg/event -count=1` → `ok`；`cd frontend && corepack pnpm test -- forebrainGatewayRuntime` → 通过。

### 第 3 步：通道与统一的执行包装

按"设计"第 1、2 条。所有现有执行入口改走 `runSubagentExecution`。

**验证**：
- `TestModelContinuationWaitsForTheUsersExecutionOfTheSameSubagent`：用户执行进行中（测试 LLM 卡住），模型的 `subagent_continue` 不会并发开始；放行后依次执行，worker 会话的行按顺序、不交错。
- `TestSubagentReceivesASteerAtItsNextToolBoundary`：subagent 首次派发、运行中调用工具时，向它的通道 `Steer` 一条消息；断言下一次模型请求的尾部带着这条消息，且发布了 `subagent_input_delivered`，且主会话的运行时没有收到它。
- `CGO_ENABLED=1 go test -race -tags fts5 ./pkg/run -run 'Subagent' -count=1` → 无 `DATA RACE`。

### 第 4 步：用户入口

按"设计"第 3、4 条。

**验证**（`pkg/run/subagent_test.go`）：
1. `TestUserMessageContinuesAFailedSubagent`：subagent 首次执行时第二次模型调用返回网络错误而失败；`SendToSubagent(..., "continue", steer)` 返回 `Started`；新执行的第一次请求带着失败前的全部历史和 `continue`；结束后注册表记录 `Status = ok`、`Output` 含新结论。
2. `TestUserCanTalkToEveryKindOfSubagent`：对 explore（一次性）、plan-reviewer、goal-evaluator、fork、general-purpose 各发一条消息，都返回 `Started` 并完成。
3. `TestUserSubagentExecutionNeverTouchesThePrimaryConversation`：用户驱动的执行结束后，主会话的 `fb_messages` 行数不变（D3）。
4. `TestInterruptSubagentToSendRunsThePendingSteersNext`：运行中 `Steer` 两条，`InterruptSubagentToSend` → true；`OnBoundary` 收到 `send` = 这两条（按顺序），`restore` 为空。
5. `TestWithdrawBeforeTheSubagentAnswers`：用户消息开始的执行在测试 LLM 尚未输出时 `WithdrawSubagentInput` → 返回这条消息；worker 会话里它的行 `visibility = 'withdrawn'`；没有 `subagent_ended` 事件。模型已输出后再调 → `false`。
6. `TestUserMessageReusesTheSubagentsPrefix`：见"缓存影响"。
7. `TestSubagentOfAnotherConversationIsNotFound`：用另一个对话 id 调 `SendToSubagent` → `ErrSubagentNotFound`。

`CGO_ENABLED=1 go test -tags fts5 ./pkg/run -count=1` → `ok`。

### 第 5 步：全量

**验证**：`gofmt -l pkg cmd` 无输出；`go vet -tags fts5 ./...` 退出码 0；`CGO_ENABLED=1 go test -tags fts5 ./... -count=1` 全部 `ok`；`CGO_ENABLED=1 go test -race -tags fts5 ./pkg/run -count=1` 无竞态；架构测试、包依赖图、死代码检查通过（新导出函数要在 007/008 里被用上；在那之前 `deadcode` 会报它们——这是预期的，在报告里列出，007/008 完成后必须清零）。

## 完成标准

- [ ] 第 1–4 步的测试全部通过，全量与竞态测试通过
- [ ] `grep -n "WithoutTurnInputRuntime" pkg/run/subagent.go` 不再出现在 subagent 执行路径上（只剩它的定义和对父运行时的说明，若有）
- [ ] 所有 subagent 执行入口都经过 `runSubagentExecution`（`grep -n "executeSubagent(\|runForkSubagent(\|continueForkSubagent(" pkg/run/subagent.go` 的每个调用点都在它的回调里）
- [ ] 架构测试通过（`Runner` 字段和导出方法数不变）
- [ ] README 状态行已更新

## STOP 条件

- 计划 004 的 `InputQueue` 没有"按会话保存""Attach/Detach""Next(Boundary)"这些能力。
- 执行结束时通道没有 surface 却有排队输入（不变式被破坏）。
- 主会话撤回的处理与"撤回的执行当作没发生过"不一致。
- 竞态测试报告 `DATA RACE` 且两次修复无效。

## 维护说明

- subagent 的任何新执行入口都必须经过 `runSubagentExecution`，否则用户发给它的 steer 会丢失、模型和用户会并发写同一个 worker 会话。
- 通道是进程内状态（和主会话的运行控制器一样）；gateway 多副本时同一个 subagent 的两次请求必须落到同一副本，这与主会话的现状相同，见 README"考虑过但不做的"。
