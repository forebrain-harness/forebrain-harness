# 计划 011：因用量上限中断的 subagent 在额度恢复后自动继续，语义与主 agent 一致

> **执行者须知**：逐步执行。每一步都运行"验证"命令并确认结果后再进入下一步。出现"STOP 条件"里的任何情况，立即停止并报告。
> 完成后更新 `docs/plan/SUBAGENT_CONVERSATION/README.md` 里本计划的状态行。**不要提交代码。** 先读 README 的"全局规则"。
>
> **前置检查**：README 里计划 005、007、008 必须都是 `DONE`，否则 STOP（本计划用 005 的用户输入通道启动继续，用 007/008 建好的 subagent 视图 composer 显示提示和接收 Esc）。
>
> **漂移检查**：
> `git diff --stat bda9505 -- pkg/turn/submit.go pkg/turn/service.go pkg/run/subagent.go pkg/process/one_shot.go pkg/process/open.go pkg/event/run_events.go pkg/tui/notify.go pkg/tui/run.go pkg/tui/render.go pkg/tui/chat_session.go pkg/gateway/run_control.go pkg/gateway/api_extra.go frontend/src/components/chat/AutoContinueBanner.vue frontend/src/composables/useChatStream.ts frontend/src/views/ChatView.vue`
> 前置计划会改其中多数文件，这是预期的；按函数名核对"现状"。

## 状态

- **优先级**：P1（owner 2026-10-04 追加的第 7 点）
- **工作量**：M
- **风险**：MED（定时器、并发：主 agent 和 subagent 可能同时撞上同一个用量上限）
- **依赖**：005、007、008
- **类别**：direction（与主 agent 对齐）
- **基线**：提交 `bda9505`，2026-10-04

## owner 的要求（逐字）

> 因为usage limit reach而中断的subagent必须可以自动恢复，对齐primary agent的语义，给subagent加上相同功能

## 为什么要做

主 agent 的回合因用量上限（服务商返回额度用尽并给出恢复时间）失败时，引擎会安排一次自动继续：composer 下方显示"额度恢复后继续"的提示，用户可以按 Esc 或开始打字取消；到点后以固定提示词 `turn.AutoContinuePrompt` 作为新回合发出。见 `pkg/turn/submit.go`（`autoContinuer`、`turnEnded`、`planFor`、`tick`、`cancel`）和 FOREBRAIN.md 里"auto-continue"一句。

subagent 没有这个能力，原因有两层：

1. 自动继续挂在 `turn.Service.Submit` 上，而 subagent 的执行根本不经过 `Submit`（它走 `run.Run` / `RunFork`）。
2. 即使经过，`planFor` 也明确排除了子运行：`if err == nil || sessionID == "" || strings.TrimSpace(req.ParentRunID) != "" || ...`。

计划 002 之后 subagent 的上下文已经落库，计划 005 之后用户可以从视图里让它继续——所以"额度恢复后自动继续"可以完全复用主 agent 的调度器，只是继续的目标换成这个 subagent。

## 语义（对齐主 agent，逐条对应）

| 主 agent（今天） | subagent（本计划） |
| --- | --- |
| 回合失败且错误是 `llm.ExplainRateLimitQuota` / `ExplainRateLimitThrottle`、带恢复时间 → 安排继续，时间 = 恢复时间 + 5 秒宽限，至少 10 秒后 | subagent 的一次执行（首次派发、模型 `subagent_continue`、用户发起、自动继续本身）以同样的错误失败 → 安排它自己的继续，时间规则相同 |
| 连续继续最多 5 次，之后不再安排 | 同，按 subagent 计数 |
| 只对 TUI 和网页的回合生效（渠道集成不安排，因为没人能取消） | 只对属于 TUI 或网页对话的 subagent 生效 |
| 任何新回合到达这个会话 → 取消（`AutoContinueSuperseded`） | 任何新执行到达这个 subagent（用户发消息、模型 `subagent_continue`）→ 取消 |
| 提示显示在 composer 下方，只在主视图显示（`autoContinueNoticeLineLocked`：`r.activeView != ""` 时不画） | 提示只在**该 subagent 的视图**里显示（README 全局规则 9） |
| 主视图里：Esc（没有在途输入时）或开始打字 → 取消 | 该 subagent 的视图里：Esc 或开始打字 → 取消。Esc 的优先级：撤回 → 中断并发送 → **取消自动继续** → 返回主视图（D1 的原则："有在途的东西时 Esc 作用于该 subagent"） |
| 到点：以 `AutoContinuePrompt` 作为新回合发出 | 到点：以 `AutoContinuePrompt` 作为这个 subagent 的一条用户消息发出（计划 005 的 `SendToSubagent`），它会出现在 subagent 视图里 |
| 生命周期事件 `auto_continue_scheduled/started/cancelled` | 同样的事件，载荷加 `agent_id`，路由到 subagent 视图 |
| 网页：`AutoContinueBanner`，可取消 | 网页的 subagent 视图里显示同一个横幅，可取消 |

D3 照旧：自动继续的结果不注入主会话。

## 现状（2026-10-04 工作区的事实）

- `pkg/turn/submit.go`：`AutoContinuePlan{SessionID, RunID, Origin, Code, Plan, ResetAt, ContinueAt, Attempt}`；`autoContinuer` 的 `pending`、`fired` 都按 `SessionID` 键；`turnStarting`（取消）、`turnEnded`（安排）、`planFor`（判断错误、排除子运行）、`armLocked`/`tick`（分片计时、到点调用 `cfg.Continue`）、`cancel`、`publish`；`Service.PendingAutoContinue`、`CancelAutoContinue`、`StopAutoContinue`。
- `AutoContinueConfig{Continue, Events, Surfaces}`。TUI：`pkg/tui/notify.go` 里 `turn.WithAutoContinue(turn.AutoContinueConfig{Continue: s.continueAfterUsageLimit, Events: event.SinkFunc(s.publishRunEvent), Surfaces: []turn.Surface{turn.SurfaceTUI}})`；gateway：`pkg/gateway/run_control.go:571` `autoContinueConfig()`，`continueAfterUsageLimit` 以分离运行启动继续。
- TUI 的显示与取消：`pkg/tui/notify.go:1706` `handleAutoContinueNotification`（`AutoContinueScheduledMsg` / `CancelledMsg` / `DueMsg`，都按 `state.sessionID` 过滤）；`streamState.autoContinue`、`cancelAutoContinue`、`takeAutoContinueDue`；`Renderer.SetAutoContinueNotice`、`autoContinueNoticeLineLocked`。
- 事件：`pkg/event/run_events.go:186-217` 三个载荷，没有 agent 字段。
- 网页：`frontend/src/components/chat/AutoContinueBanner.vue`（含测试）；取消走 WS op `cancel_auto_continue`（wsOp 常量在 `pkg/gateway/run_control.go:562`，处理函数 `handleCancelAutoContinueMessage` 在 `:758-761`）或 `api.Delete("/auto-continue", ...)`（`pkg/gateway/api_extra.go:198`）。
- 计划 005 之后：`run.SendToSubagent`、`run.SubagentSurface`、所有 subagent 执行都经过 `runSubagentExecution`。

## 设计

### 1. 调度器认得"某个 subagent"

`pkg/turn/submit.go`：

- `AutoContinuePlan` 加 `AgentKey string`（为空表示主会话）和 `WorkerSessionID string`。新增私有方法 `key()`：`AgentKey` 非空时返回 `WorkerSessionID`，否则返回 `SessionID`。`pending`、`fired` 一律按 `key()` 键。`SessionID` 始终是**对话**的会话 id（事件按它归档，界面按它路由）。
- `Payload()` 给 `AutoContinueScheduledPayload` 填 `AgentID`（见第 4 条）；`publish` 给 started/cancelled 载荷也填。
- 新增 `Service` 方法（turn.Service 不受 `Runner` 的方法上限约束）：

  ```go
  // SubagentExecutionStarting supersedes a subagent's pending continuation:
  // a new execution reaching it — the user's message, the model's
  // subagent_continue — moves its conversation on.
  func (s *Service) SubagentExecutionStarting(ctx context.Context, workerSessionID string)

  // SubagentExecutionEnded arms a subagent's continuation when its execution
  // stopped on a usage limit with a known reset, by the same rules a primary
  // turn is armed by, and otherwise closes the subagent's run of continuations.
  func (s *Service) SubagentExecutionEnded(ctx context.Context, end SubagentExecutionEnd)
  ```

  `SubagentExecutionEnd{ConversationSessionID, WorkerSessionID, AgentKey, RunID string; Origin Origin; Err error}`。实现复用 `planFor` 的错误判断和时间计算：把 `planFor` 拆成"判断 + 算时间"的私有函数，主会话和 subagent 都调它；**不要**复制一份。子运行的排除条件只对主会话路径保留（它防的是"主会话的 Submit 误把子运行当成自己的回合"，与 subagent 路径无关）。
- `PendingAutoContinue(key)` / `CancelAutoContinue(ctx, key, reason)` 的参数语义改为"计划的键"（主会话传会话 id，subagent 传 worker 会话 id）；更新注释。

### 2. 引擎把 subagent 的执行起止告诉调度器

`pkg/run` 不能 import `pkg/turn`。沿用计划 002 的做法，经执行器接口交给组合根：`run.SubagentExecutor` 加

```go
	// SubagentExecutionStarting and SubagentExecutionEnded report one execution
	// of a subagent to whatever schedules its continuations.
	SubagentExecutionStarting(ctx context.Context, workerSessionID string)
	SubagentExecutionEnded(ctx context.Context, end SubagentExecutionEnd)
```

（`run.SubagentExecutionEnd` 与 turn 的同名结构字段相同，`Origin` 用表示 surface 的字符串，process 负责转换。）`runSubagentExecution`（计划 005）在拿到执行权之后调 `Starting`，在执行结束（任何结果，含成功）之后调 `Ended`——成功时调用的意义是"关闭连续继续的计数"，与主会话 `turnEnded(..., nil)` 相同。

`process.Environment` 实现这两个方法：转发给已登记的回调 `env.OnSubagentExecution`（新字段，类型是一个含两个函数的小结构）。TUI 和 gateway 在创建会话时把各自 `turn.Service` 的 `SubagentExecutionStarting/Ended` 登记上去（与 `env.OnConfigReload` 的登记方式相同，见 `pkg/tui/notify.go` 里 `env.OnConfigReload = func(...)`）。没有登记（例如一次性执行、渠道）就什么都不做——这正是"渠道不安排自动继续"的结构性实现，不需要再判断 surface。

`Origin`：subagent 所属对话的 surface。`runSubagentExecution` 从上下文里取（执行者找出主会话回合把 surface 放进上下文的方式，例如 `hook.HookContext.Channel` 或 `turn.Origin`；如果上下文里没有可靠的来源，就由 surface 在登记回调时声明自己是哪个 surface，回调只为自己的对话安排继续）。

### 3. 到点：继续这个 subagent

两个 surface 的 `Continue` 端口（TUI `continueAfterUsageLimit`、gateway `continueAfterUsageLimit`）开头加一个分支：`plan.AgentKey != ""` 时走 subagent：

- gateway：直接 `run.SendToSubagent(ctx, runner, <gateway 的 SubagentSurface>, plan.SessionID, plan.AgentKey, run.Input{Text: prompt, Parts: []llm.ContentPart{llm.Text(prompt)}}, run.TurnInputModeSteer)`；返回 `SubagentDeliveryStarted` 以外的结果（例如它已被别的执行接手）就返回 `turn.ErrAutoContinueUnavailable`。
- TUI：与主会话一样经过事件循环，以便"按 Esc 的同时计时器到点"的竞争由界面裁决：`AutoContinueDueMsg` 带上 `AgentKey`；`handleAutoContinueNotification` 只在**该 subagent 的**待继续状态仍在时，调用 `SendToSubagent`（而不是 `executeComposerSubmission`）。
- 两处共用的"把 subagent 的继续发出去"的那一行调用，提成 `process` 里的一个函数（例如 `(*Environment).ContinueSubagent(ctx, surface, plan, prompt) error`），两个 surface 都调它，不各写一份。

### 4. 事件

`pkg/event/run_events.go`：`AutoContinueScheduledPayload`、`AutoContinueStartedPayload`、`AutoContinueCancelledPayload` 各加 `AgentID string \`json:"agent_id,omitempty"\``。Go/TS 共享 fixture 加例子。

### 5. TUI

- `streamState.autoContinue` 从"一个"改为"按视图键的映射"（`""` 为主会话，roster key 为 subagent）；`handleAutoContinueNotification` 按消息的 `AgentID` 存取。
- `Renderer.autoContinueNotice` 同样改为按视图键保存；`autoContinueNoticeLineLocked` 画**当前视图**的那一条（删掉 `r.activeView != ""` 时不画的特判，换成"取当前视图键的提示"）。
- 取消：在 subagent 视图里开始打字、或按 Esc（优先级见上表），调用 `Service.CancelAutoContinue(ctx, <worker 会话 id>, turn.AutoContinueCancelledByUser)`。worker 会话 id 从 roster 行或注册表记录取（计划 007 在 subagent 视图的状态里已经持有它；若没有，用 `agent.GetMerged` 查）。
- `announceAutoContinue`（把提示也写进记录的那一行）按视图写进该 subagent 的视图。

### 6. 网页

- `useChatStream.ts`：`auto_continue_*` 事件按 `agent_id` 存到对应视图的状态。
- `ChatView.vue`：subagent 视图显示它自己的 `AutoContinueBanner`；取消时 WS op `cancel_auto_continue` 带上 `agent_id`；gateway 的 `handleCancelAutoContinueMessage` 和 `DELETE /auto-continue` 接受可选的 `agent_id`，有它时按 worker 会话 id 取消（先校验该 subagent 属于这个对话、这个主 agent）。
- 绑定会话时 gateway 会把待继续的计划补发给新打开的页面（`AutoContinuePlan.Payload()` 的注释）；改为把该对话及其所有 subagent 的待继续计划都补发。

## 缓存影响

自动继续发出的是追加在 subagent worker 会话尾部的一条用户消息，前缀与它上一次请求相同（计划 002、005 已保证）。没有新的前缀变化。

## 范围

**只改这些文件：** `pkg/turn/submit.go`、`pkg/turn/service.go`（如需）、`pkg/run/subagent.go`、`pkg/process/one_shot.go`、`pkg/process/open.go`（`Environment` 字段，如 `OnConfigReload` 定义在别处则改那个文件）、`pkg/event/run_events.go`、`pkg/tui/notify.go`、`pkg/tui/run.go`、`pkg/tui/render.go`、`pkg/tui/chat_session.go`、`pkg/gateway/run_control.go`、`pkg/gateway/api_extra.go`、`frontend/src/composables/useChatStream.ts`、`frontend/src/views/ChatView.vue`、`frontend/src/components/chat/AutoContinueBanner.vue`（如需）、`frontend/src/lib/forebrainGatewayRuntime.ts`、`frontend/src/locales/index.ts`（如需新文案），以及它们的测试；所有 `SubagentExecutor` 测试替身。

**不要动：** 主会话自动继续的任何可观察行为（第 1 步的测试钉住它）；`AutoContinuePrompt` 的文字（它是字节稳定的提示词）。

## 步骤

### 第 1 步：钉住主会话的现行行为

`pkg/turn` 里已有的自动继续测试全部保留，先跑绿钉住现行行为。`TestPrimaryAutoContinueIsUnchangedBySubagentPlans`（主会话和它的一个 subagent 同时有待继续计划时，主会话的计划、取消、到点行为与只有主会话时完全相同）**放到第 2 步调度器落地后再写**——现在写它编译不过，整包没法"只跑已有的"。

**验证**：`CGO_ENABLED=1 go test -tags fts5 ./pkg/turn -run 'AutoContinue' -count=1` → `ok`（只含已有测试）。

### 第 2 步：调度器

按"设计"第 1、4 条。新增测试（`pkg/turn`）：
- `TestPrimaryAutoContinueIsUnchangedBySubagentPlans`（第 1 步说明的那条，调度器落地后补上）。
- `TestSubagentUsageLimitArmsItsOwnContinuation`：`SubagentExecutionEnded` 带额度错误 → 有按 worker 会话 id 键的计划，`SessionID` 是对话 id，事件载荷带 `agent_id`；主会话没有计划。
- `TestSubagentContinuationIsSupersededByItsNextExecution`：`SubagentExecutionStarting(worker)` → 计划取消，原因 `superseded`。
- `TestSubagentContinuationsStopAfterFiveInARow`。
- `TestSubagentSuccessResetsItsContinuationCount`。

**验证**：`CGO_ENABLED=1 go test -tags fts5 ./pkg/turn -count=1` → `ok`。

### 第 3 步：引擎上报与组合根接线

按"设计"第 2 条。`pkg/run` 加 `TestEverySubagentExecutionIsReportedToItsScheduler`（首次派发、模型继续、用户发起各一次，替身记录 Starting/Ended 调用）。

**验证**：`CGO_ENABLED=1 go test -tags fts5 ./pkg/run ./pkg/process -count=1` → `ok`；架构测试 `ok`（`pkg/run` 没有 import `pkg/turn`）。

### 第 4 步：到点继续

按"设计"第 3 条。`pkg/gateway` 加 `TestGatewayContinuesASubagentAfterItsUsageLimit`；`pkg/tui` 加 `TestTUIContinuesASubagentOnlyWhileItsNoticeIsShown`（Esc 取消后到点的消息被丢弃）。

**验证**：`CGO_ENABLED=1 go test -tags fts5 ./pkg/gateway ./pkg/tui -run 'AutoContinue' -count=1` → `ok`。

### 第 5 步：TUI 与网页的显示和取消

按"设计"第 5、6 条。TUI 加 `TestAutoContinueNoticeBelongsToTheViewOfItsAgent`（提示只在该 subagent 视图出现；在该视图打字或按 Esc 取消它；主视图的提示不受影响）。网页加组件/组合式测试：subagent 视图显示横幅、取消时带 `agent_id`。

**验证**：`CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -count=1` → `ok`；`cd frontend && corepack pnpm test` → 通过；`cd frontend && corepack pnpm exec vue-tsc --noEmit -p tsconfig.json` → 退出码 0。

### 第 6 步：真机

在 `.claude/skills/run-forebrain/fake_provider.py` 加模式 `subagent-limit`，和计划 007 的 `subagent-net` 一样按**请求内容**分辨主 agent 和 subagent：subagent 的请求（system 含 `You are a general-purpose subagent`）在第一次 subagent 请求之后 `FAKE_LIMIT_SECONDS`（默认 20）秒内按 `limit` 模式返回 429（带 `resets_in_seconds`），过后按 `reply` 回答；主 agent 的第一个请求以 `subagent_run` 回答，之后回答固定文本 `primary noted the failure`。`driver.sh` 的用法和 `SKILL.md` 的模式表各加一行。

```bash
D=.claude/skills/run-forebrain/driver.sh
$D reset; FAKE_LIMIT_SECONDS=20 $D start subagent-limit
$D submit 'delegate it'
$D wait 'spawned' 30
# 进入 subagent 视图（roster 回车或点卡片），看到自动继续提示
$D screen
sleep 30
$D screen   # subagent 视图里出现自动继续的那条用户消息和回答
```

网页同理，在计划 009 的网页场景里覆盖。

**验证**：第一次 `screen` 在 subagent 视图的 composer 下方有自动继续提示，主视图没有；第二次 `screen` 在 subagent 视图里有 `The previous response was interrupted by a usage limit…` 那条消息和之后的回答。屏幕输出贴进报告。

## 完成标准

- [ ] 第 1–5 步测试全部通过；全量 Go、前端测试、类型检查、架构测试、死代码检查通过
- [ ] `planFor` 的错误判断和时间计算只有一份实现（主会话与 subagent 共用）
- [ ] 第 6 步的屏幕输出已附在报告里
- [ ] README 状态行已更新

## STOP 条件

- 主会话自动继续的任何已有测试需要改断言。
- 找不到可靠的方法得知 subagent 所属对话的 surface，且 surface 自报的方式会让一个 surface 为另一个 surface 的对话安排继续。
- 需要 `pkg/run` import `pkg/turn`。

## 维护说明

- 以后主会话自动继续的规则（宽限、下限、次数上限、可安排的错误种类）只改一处，subagent 自动跟随。
- 主 agent 和它的 subagent 同时撞上同一个额度时，会各自安排一次继续；主 agent 的继续如果调用了 `subagent_continue`，会经由"新执行到达即取代"取消 subagent 自己的那次。评审时确认这条路径有测试（第 2 步第二条）。
