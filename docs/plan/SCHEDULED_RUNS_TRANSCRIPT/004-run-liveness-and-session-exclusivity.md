# 计划 004：运行的存活由进程租约证明，一个会话同一时刻只有一个活着的主运行

> **执行者须知**：逐步执行。每一步都运行"验证"命令并确认结果后再进入下一步。出现"STOP 条件"里的任何情况，立即停止并报告。
> 完成后更新 `docs/plan/SCHEDULED_RUNS_TRANSCRIPT/README.md` 里本计划的状态行。**不要提交代码。** 先读 README 的"全局规则"。
> **开始前确认计划 001 已 DONE**：本计划要改 001 引入的 `startDetachedTurn`，并删掉 001 临时加的两处预检。
>
> **漂移检查（先运行）**：
> `git diff --stat 1a6d708..HEAD -- pkg/state/runrt.go pkg/state/schema.sql pkg/state/schema_migrations.go pkg/run/supervisor.go pkg/process/open.go pkg/turn/events.go pkg/turn/submit.go pkg/turn/scheduler.go pkg/gateway/run_control.go pkg/gateway/server.go pkg/gateway/approval.go pkg/gateway/api_extra.go pkg/gateway/serve_run.go pkg/tui/run.go pkg/tui/notify.go pkg/tui/chat_session.go`
> 计划 001–003 改过其中一部分，`pkg/process/open.go` 还被并行的 LSP 计划改过（见 README"与并行工作的关系"），这些都是预期的。下文标了"（001 后）"的摘录指 001 完成后的形状；其余按内容核对，不按行号。

## 状态

- **优先级**：P1（owner 2026-10-03："D8必须写进本期计划"）
- **工作量**：L
- **风险**：MED-HIGH（每一个创建主运行的入口都会受这条新规则约束；有一次 schema 迁移）
- **依赖**：计划 001
- **类别**：bug
- **基线**：提交 `1a6d708`，2026-10-03

## 为什么要做

同一台机器上的每个 forebrain 进程（gateway、一个或多个 TUI）共用一个状态库。`pkg/state/runrt.go` 里 `ListUncertainResolvedWaits` 的注释写的就是这件事："every forebrain on this machine shares one state database"。生产环境的 gateway 还是多副本部署。可是运行（run）的两条基本事实今天只存在进程内存里：

1. **一个运行是否还活着。** `fb_runs.status = 'running'` 只说明"某个进程曾经开始了它"。进程崩溃、被 kill，或者 TUI 在回合进行中退出，这一行就永远停在 `running`。仓库里没有任何代码去收拾它（`grep -rn "RunStatusRunning" pkg` 核实过：只有设置，没有回收）。后果：
   - 这个回合在网页和终端重放里没有结尾，没有错误块，也没有 "Worked for" 行；
   - `ActivePrimaryRun` 把它当成"正在运行"；
   - 计划 005 之后，定时任务的执行记录会永远停在"执行中"。
2. **一个会话里是否已经有一个活着的回合。** 今天只有进程内的两道闸：`session.Locker`（gateway 的 `Submit`、TUI 的 `DispatchSurfaceTurn`）和 `run.Controller.Find`。跨进程没有任何互斥。TUI 和 gateway 打开同一个会话时，两边可以同时往里跑回合，对话记录交错，上下文前缀被打乱。001 的心跳"会话忙就跳过"也只看得到本进程。

另有一个同类缺陷：在挂着审批的会话里发消息，网页路径**先建运行、再调 `Submit`**。`Submit` 因审批闸门拒绝时返回 `TurnWaitingApproval` 且 `Resume == nil`，网页路径却把它当成功处理：写一个空回答，那一行运行永远停在 `running`（`pkg/gateway/server.go` 的 WS 发送路径）。

根因：运行的"存活"和会话的"独占"都没有落到共享存储上。修复沿用仓库里已有、已被验证的做法——审批续跑的进程租约（`fb_run_waits.resume_owner` / `resume_claimed_at_ms`、`WaitResumeLease`、`claimWaitResume` 的单条件 UPDATE、`holdWaitResumeLease` 定时续租）——把它推广到运行本身：

- 每个进程一个属主（owner），在库里登记并定时续租。运行记下自己的属主。属主租约过期的 `running` 运行即被视为**已放弃**，由任何一个活着的进程收尾：标记失败、补上运行时钟、发出结束事件。
- 创建主运行时用**一条原子语句**检查"这个会话没有活着的主运行"（停在审批上，或属主仍存活的 running），否则拒绝。这样同一个规则对所有进程、所有入口成立，进程内那几道预检就成了多余，可以删掉。

## 现状

### 运行存储（基线）

`pkg/state/runrt.go`：

- `type RunStore struct { DB *sql.DB }`。生产代码里只在 `pkg/process/open.go` 构造一次：`runSvc := &state.RunStore{DB: sqlDB}`。测试里有 113 处 `RunStore{DB: ...}`。
- `CreateRun(ctx, sessionID, inputText)`：直接 `INSERT INTO fb_runs(... status ...) VALUES(..., 'running', ...)`，没有任何检查。
- `CreateSubagentRun(ctx, parentRunID, sessionID, preview)`：同样直接插入，带 `parent_run_id`。
- `SetStatus(ctx, runID, st)`：`UPDATE fb_runs SET status=?, updated_at=? WHERE id=?`，无条件，任何状态都能改成任何状态。
- `WaitResumeLease = 5 * time.Second`、`claimWaitResume`（单条件 UPDATE 加"过期即可接管"）、`resumeLeaseFresh`：这是本计划要照抄的租约写法。

`pkg/state/schema.sql` 的 `fb_runs`：

```sql
CREATE TABLE fb_runs (
  id TEXT PRIMARY KEY,
  session_id TEXT NOT NULL REFERENCES fb_sessions(id) ON DELETE CASCADE,
  parent_run_id TEXT REFERENCES fb_runs(id) ON DELETE CASCADE,
  input_text TEXT NOT NULL,
  status TEXT NOT NULL CHECK (status IN ('running', 'waiting_action', 'done', 'failed', 'cancelled')),
  ... usage_* ...,
  started_at_ms INTEGER, finished_at_ms INTEGER, worked_ms INTEGER,
  created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL,
  CHECK ((started_at_ms IS NULL) = (finished_at_ms IS NULL) AND (finished_at_ms IS NULL) = (worked_ms IS NULL))
) STRICT;
CREATE INDEX idx_fb_runs_active ON fb_runs(status, updated_at DESC) WHERE status IN ('running', 'waiting_action');
```

迁移：`pkg/state/schema_migrations.go` 的 `stateSchemaVersion = 4`，`migrateStateV3ToV4` 是"加一列"的范例。测试范例是 `pkg/state/schema_migrations_test.go` 的 `TestStateV3UpgradesToV4WithSessionsIntact`。

### 创建主运行的入口（基线，`grep -rn "\.CreateRun(" pkg --include='*.go' | grep -v _test`）

1. `pkg/run/supervisor.go` 的 `Run`：没有 `SuperviseExistingRunID` 时，`ParentRunID` 为空就 `CreateRun`，失败返回 `fmt.Errorf("create run: %w", err)`。走这里的有 TUI 回合、渠道回合（`Core.Submit` → 执行器 → `run.Run`），以及兜底的子代理会话。
2. `pkg/gateway/server.go` 的 WS 发送路径：`rr, err := s.RunRT.CreateRun(r.Context(), sid, content)`，失败时 `writeMsg(wsServerMsg{Op: "turn_withdrawn", ..., Error: err.Error()})` 并 `continue`。网页收到 `turn_withdrawn` 会把消息完整放回输入框并显示错误。
3. （001 后）`pkg/gateway/run_control.go` 的 `startDetachedTurn`：先用 `s.runController().Find(sid)` 和 `s.Core.ParkedOnApproval(ctx, sid)` 预检，再 `CreateRun`。`runDetachedTurn` 里还有一个"`Submit` 因别的运行的审批而拒绝"的分支（`TurnWaitingApproval && Resume == nil`）。

### 把运行改回 `running` 的地方（基线，6 处）

`pkg/tui/chat_turn.go` 两处（审批后续跑）、`pkg/tui/chat_surface.go` 一处、`pkg/run/subagent.go` 一处（子代理续跑）、`pkg/gateway/api_extra.go` 的 `resumeGatewayRun`、`pkg/gateway/approval.go` 一处。全部是 `SetStatus(ctx, id, state.RunStatusRunning)`，都是从 `waiting_action` 恢复。

### gateway 的运行结束漏斗（基线）

`pkg/gateway/run_control.go` 的 `finishRun(ctx, sessionID, runID)`，注释："every way a gateway run ends goes through it"。它释放未取走的输入、盖运行时钟、`runController().Finish`，但**不设置状态**。状态由各调用点自己设，其中两处是**先报告结束、后设失败**：

- WS 发送路径的错误分支：`s.finishRun(...)` → `writeMsg(run_error)` → `s.RunRT.SetStatus(agCtx, runID, state.RunStatusFailed)`。
- （001 后）`runDetachedTurn` 的错误分支：`s.finishRun(...)` → 发 `turn_error` → `SetStatus(failed)`。

执行器内部失败的运行，`run.Run` 的 `settleRunError` 已经先设了 failed。但执行器在 `run.Run` 之前就失败的情况（例如必需的 MCP 服务没起来，`FailOnRequiredMCP`），状态只能靠这两处事后设置。加上会话独占以后，用户收到错误后立刻重发，会被自己刚结束的那个运行挡住，报"正在运行"。

### 进程与恢复（基线）

- `pkg/process/open.go`：`Open` 构造 `Environment`；`Close`（`open.go:93` 起）按顺序关 runner、runner 池、LSP、遥测、SQL。
- 审批续跑属主：gateway 是 `s.approvalResumeOwner()`，即 `"gateway:" + uuid`（`pkg/gateway/server.go`）；TUI 是 `s.approvalResumeOwner()`，即 `"tui:" + uuid`（`pkg/tui/chat_session.go:171-177`）。两者都是"每进程一个 uuid"。
- 启动时的审批恢复：gateway 是 `recoverResolvedApprovalWaitsOnce`（`pkg/gateway/api_extra.go`），TUI 是 `recoverResolvedApprovalWaitsOnce(sessionID)`（`pkg/tui/chat_session.go`）。它们用 `ListUncertainResolvedWaits` 处理"执行已越过围栏、但续租已停"的运行：标记失败并发 `turn_error`。**带 `fb_run_waits` 行的运行归它们管。**
- 两个面各有一个发布运行事件的函数，签名相同：`func(ctx context.Context, sessionID, runID, eventType string, payload any) error`。gateway 是 `(*Server).publishGatewayRunEvent`（`pkg/gateway/approval.go`，会给结束类 payload 补上计划清单事实），TUI 是 `(*ChatSession).publishTUIRunEvent`（`pkg/tui/notify.go`，持久化到会话事件日志）。
- 子代理的 `subagent_spawned` 事件以**子运行的 id** 作 `RunID` 发布（`pkg/run/subagent.go`，`publishEventWithID(..., entry.RunID, entry.SessionID, event.RunEventSubagentSpawned, ...)`），可以用 `RunStore.ListRunEventsOfTypes(ctx, childRunID, event.RunEventSubagentSpawned)` 按子运行找回。
- TUI 的"撤回"机制（Esc 在模型出声之前按下）：`pkg/tui/run.go` 里的 `foreground.withdraw()` → `renderer.withdrawSubmission(foreground)` → `state.restoreWithdrawnWork(session)` → `renderComposerWithState(...)`。效果是用户卡片被移除，文字完整回到输入框。

## 设计

### 1. 存储：属主、租约、原子建运行、放弃回收（`pkg/state/runrt.go`，不新建文件）

- 迁移 v5（`schema_migrations.go` 加 `migrateStateV4ToV5`，`stateSchemaVersion = 5`；`schema.sql` 同步改，供新库使用）：

  ```sql
  ALTER TABLE fb_runs ADD COLUMN owner TEXT NOT NULL DEFAULT '';
  CREATE TABLE fb_run_owners (
    owner TEXT PRIMARY KEY CHECK (TRIM(owner) <> ''),
    heartbeat_at_ms INTEGER NOT NULL
  ) STRICT;
  CREATE INDEX idx_fb_runs_session_live ON fb_runs(session_id)
    WHERE parent_run_id IS NULL AND status IN ('running', 'waiting_action');
  ```

  `schema.sql` 里 `owner` 放在 `updated_at` 之后、表级 `CHECK` 之前，并加注释：属主是正在驱动这个运行的进程，只对 `running` 有意义。基线库里已有的 `running` 行迁移后 `owner = ''`，第一次回收时会被当作已放弃收尾。这正是要修的历史遗留：那些行的进程早就不在了。迁移函数和 v4 一样要能在"表里已经有这列/这个表"时跳过。
- 常量与类型：

  ```go
  // RunOwnerLease is how long a process may go without renewing its
  // registration before its running runs read as abandoned. It is generous on
  // purpose: a process starved of the write lock for a few seconds is alive,
  // and reaping a live run is far worse than reaping a dead one a minute late.
  const RunOwnerLease = 60 * time.Second
  ```

  `RunStore` 新增字段 `Owner string`，注释："the process this store writes runs for. Every run it creates, and every run it moves back to running, is stamped with it; a store without one creates runs no live process vouches for."
- `func (s *RunStore) HoldOwnerLease(ctx context.Context) (stop func(), err error)`：先**同步** upsert 一次 `fb_run_owners(owner, heartbeat_at_ms=now)`，失败就返回错误；然后起协程每 `RunOwnerLease / 6` 续租一次（续租失败只记日志，下一次再试）。`stop` 结束协程并删除本属主的行，于是本进程没收尾的运行立即被视为已放弃。`s.Owner == ""` 时返回错误。
- 会话忙的错误，文案就是给人看的那一句：

  ```go
  // ErrSessionBusy is the family of refusals to start a turn in a session that
  // already has one alive; errors.Is matches both members below.
  var ErrSessionBusy = errors.New("session busy")

  // ErrSessionRunning: another live run is driving the conversation.
  // ErrSessionAwaitingApproval: a run in it is parked on an approval.
  ```

  两个成员的 `Error()` 分别是：
  - `This conversation is already running a turn; send again when it finishes.`
  - `This conversation is waiting for an approval; answer it before sending another message.`

  实现方式不限（自定义类型实现 `Is(target) bool` 即可）。
- `CreateRun` 改成一条原子语句：

  ```sql
  INSERT INTO fb_runs(id, session_id, input_text, status, owner, created_at, updated_at)
  SELECT ?, ?, ?, 'running', ?, ?, ?
  WHERE NOT EXISTS (
    SELECT 1 FROM fb_runs r
    WHERE r.session_id = ? AND r.parent_run_id IS NULL
      AND (r.status = 'waiting_action'
           OR (r.status = 'running' AND EXISTS (
                 SELECT 1 FROM fb_run_owners o WHERE o.owner = r.owner AND o.heartbeat_at_ms >= ?))))
  ```

  最后一个参数是 `now - RunOwnerLease`（毫秒）。插入 0 行时，再查一次会话里有没有 `waiting_action` 的主运行，返回 `ErrSessionAwaitingApproval`，否则返回 `ErrSessionRunning`。
  注意：属主已过期的 `running` 行**不挡**新运行，它们会被回收器收尾。SQLite 同一时刻只有一个写者，`INSERT ... SELECT` 是一条语句，所以检查和插入对其它进程是原子的。
- `CreateSubagentRun`：插入时带上 `owner = s.Owner`，不做独占检查（子运行属于一个活着的父运行）。
- `SetStatus` 加上状态机条件，"一个运行只结束一次"（与 `StampRunTiming` 的"先写者胜"同一原则）：
  - 设为 `running`：只对 `waiting_action` 或 `running` 的行生效，并同时写 `owner = s.Owner`；
  - 设为终态（`done`/`failed`/`cancelled`）：只对 `running` 或 `waiting_action` 的行生效；
  - 设为 `waiting_action`：只对 `running` 的行生效。

  调用方签名不变。
- `func (s *RunStore) SessionHasLiveRun(ctx context.Context, sessionID string) (bool, error)`：与 `CreateRun` 里 `NOT EXISTS` 的子查询同一个条件，抽成一个 SQL 片段常量，两处共用。给计划 005 用。
- 放弃回收：

  ```go
  // AbandonedRun is a run reaped because the process driving it stopped
  // renewing its lease.
  type AbandonedRun struct {
  	ID, SessionID, ParentRunID, Owner string
  }

  // ReapAbandonedRuns ends every running run whose owner's lease has lapsed —
  // failed, with its clock stamped from when it started to when its owner was
  // last seen — and returns them for the caller to report. A run with an
  // approval continuation row is skipped: the continuation recovery
  // (ListUncertainResolvedWaits) owns it and knows whether its tool ran.
  func (s *RunStore) ReapAbandonedRuns(ctx context.Context, now time.Time) ([]AbandonedRun, error)
  ```

  在一个事务里完成：
  1. 选出 `status='running' AND NOT EXISTS (fb_run_waits 行) AND (owner='' OR 属主行不存在 OR 属主 heartbeat_at_ms < now-RunOwnerLease)` 的行。
  2. 对每一行做 CAS：`UPDATE fb_runs SET status='failed', updated_at=? WHERE id=? AND status='running' AND owner=?`，只收改到了的行。
  3. 补时钟：`started_at_ms = created_at*1000`，`finished_at_ms = COALESCE(属主最后心跳, updated_at*1000)`（不得早于开始），`worked_ms = 两者之差`，仅当 `finished_at_ms IS NULL`。

### 2. 引擎：放弃回收器（`pkg/turn/events.go`，不新建文件）

```go
// AbandonedRunReason is what a run reaped from a stopped process says ended it.
const AbandonedRunReason = "The process running this turn stopped before it finished."

// AbandonedRunReaper ends the runs a stopped process left running, and
// reports each ending through the surface's own run-event funnel so every
// page, replay and scheduled-task record sees it the way it sees any other
// ending. Every process runs one; the reap is a compare-and-swap, so each
// abandoned run is reported exactly once, by whichever process got there.
type AbandonedRunReaper struct {
	Runs    *state.RunStore
	Publish func(ctx context.Context, sessionID, runID, eventType string, payload any) error
	// Recover re-runs the surface's own approval-continuation recovery after
	// each reap. A run that died in the middle of a continuation keeps its wait
	// row and is skipped by the reap; without this it would stay running until
	// some process restarted or reopened its session.
	Recover func(ctx context.Context)
}

func (r AbandonedRunReaper) ReapOnce(ctx context.Context)
// Start reaps once now and then every state.RunOwnerLease, until stop.
func (r AbandonedRunReaper) Start(ctx context.Context) (stop func())
```

`ReapOnce` 对每个被回收的运行：

- 主运行（`ParentRunID == ""`）：`Publish(ctx, sid, id, event.RunEventTurnError, event.TurnErrorPayload{Error: AbandonedRunReason, Message: AbandonedRunReason, Detail: &event.TurnErrorDetail{Code: "run_abandoned"}})`。网页的 `formatProviderError` 新认识 `run_abandoned`（新键 `runError.runAbandoned`：中文 `运行这个回合的进程在它完成前停止了。`，英文 `The process running this turn stopped before it finished.`），错误块在绘制时按查看者的语言显示。
- 子运行：用 `Runs.ListRunEventsOfTypes(ctx, id, event.RunEventSubagentSpawned)` 找它的 spawned 事件，按其 payload 发 `event.RunEventSubagentEnded`：`AgentID`、`AgentType`、`TaskID`、`WorkerSessionID`、`ParentRunID`、`ParentToolCallID`、`TaskIndex`、`ExecutionID` 照抄，`Status: "failed"`，`Error: AbandonedRunReason`。找不到 spawned 事件就不发（没有卡片需要收尾）。

处理完所有被回收的运行后，`Recover != nil` 时调用它。

### 3. 进程：登记属主（`pkg/process/open.go`）

- `Open` 里给唯一的生产 `RunStore` 设 `Owner = <sessionSource> + "-" + uuid`（`sessionSource` 是 `Open` 已有的值，例如 `webchat`、`tui`），紧接着 `stop, err := runSvc.HoldOwnerLease(ctx)`，出错则 `Open` 返回错误。`stop` 存进 `Environment`，在 `Close` 里于关闭 runner 和池**之后**、关闭 SQL **之前**调用。
- 审批续跑属主并入同一身份：gateway 和 TUI 的 `approvalResumeOwner()` 改为返回 `Env.Deps.RunRT.Owner`，删除各自的 `sync.Once` 和 uuid 字段。理由：一个进程只有一个"我是谁"，两个 uuid 表示同一件事，会让"这个进程还活着吗"有两个答案。
- 在 `pkg/process/open_test.go` 加一条：`Open` 之后 `env.Deps.RunRT.Owner != ""`，`fb_run_owners` 里有它的行；`Close` 之后这一行没了。

### 4. 各入口改为依赖原子建运行

- `pkg/run/supervisor.go` 的 `Run`：`CreateRun` 返回的错误 `errors.Is(err, state.ErrSessionBusy)` 时**原样返回**（不包 `create run:` 前缀，因为这句话是给人看的）；其余错误照旧包装。
- （001 后）`pkg/gateway/run_control.go` 的 `startDetachedTurn`：删掉 `Find` 和 `ParkedOnApproval` 两个预检，直接 `CreateRun`；`errors.Is(err, state.ErrSessionBusy)` 时原样返回。删除 `runDetachedTurn` 里"`Submit` 因别的运行的审批而拒绝"的分支和它的测试：会话独占成立以后，刚建好的运行不可能撞上另一个运行的审批，这个分支结构上不可达。删除 `turn.Service.ParkedOnApproval` 及其测试（再无调用方）。删除 001 加的 `turn.ErrSessionBusy`，所有用处改用 `state.ErrSessionBusy`；`pkg/turn/scheduler.go` 的 `fireHeartbeat` 判断改为 `errors.Is(err, state.ErrSessionBusy)`。`continueAfterUsageLimit` 把它映射成 `turn.ErrAutoContinueUnavailable` 的逻辑不变。
- gateway WS 发送路径：`CreateRun` 失败时已经发 `turn_withdrawn` 并带上 `err.Error()`，消息会完整回到输入框。另外要让网页用自己的语言说这句话：`pkg/state` 提供 `SessionBusyCode(err error) string`，对两个成员分别返回 `session_running`、`session_awaiting_approval`，其它错误返回 `""`；WS 路径在代码非空时把它放进 `turn_withdrawn` 的 `Data: map[string]any{"code": code}`。
- 网页（`frontend/src/composables/useChatStream.ts` 的 `turn_withdrawn` 分支，`frontend/src/lib/providerError.ts`）：撤回时除了 `withdrawnError` 还记下 `payload.data.code`。显示撤回提示的地方像 `RunErrorBlock` 那样在绘制时调用 `formatProviderError({ code })`，认得就用本地化的句子，不认得就用 `error` 原文。`formatProviderError` 新认识 `session_running`（新键 `runError.sessionRunning`：中文 `这个对话正在运行一个回合，等它结束后再发送。`，英文 `This conversation is already running a turn; send again when it finishes.`）；`session_awaiting_approval` 在 001 里已经有了。
- 渠道（`pkg/gateway/channels.go` 的 `submitChannelTurn`）：`Submit` 的错误经 `llm.ExplainError` 投递给渠道用户。`ExplainError` 对不认识的错误返回 `err.Error()`（`pkg/llm/explain.go`），所以渠道用户收到的正好是那一句。不改代码，只加测试。
- TUI：`Submit` 以 `state.ErrSessionBusy` 失败时（此时模型还没出声，用户行也还没写入，因为它在 `BeforeAgent` 里才写），按"撤回"处理：把 Esc 撤回那一段（`withdraw` → `withdrawSubmission` → `restoreWithdrawnWork` → `renderComposerWithState`）抽成一个函数，Esc 和这里共用；然后画一条 `FrameSystem` 提示，内容就是错误那一句。不得把这个错误画成运行错误块：它不是一个运行，没有运行可以收尾。

### 5. gateway：结束漏斗负责状态（`pkg/gateway/run_control.go` 及其调用点）

- `finishRun(ctx context.Context, sessionID, runID string, status state.RunStatus)`：第一件事是 `s.RunRT.SetStatus(ctx, runID, status)`（`RunRT` 非 nil 时），然后才做现有的事。注释补一句："the run's status is terminal before anything reports its end, so the next turn the person sends is never refused by the run that just told them it ended."
- 所有调用点（`grep -n "finishRun(" pkg/gateway/*.go | grep -v _test`，基线 15 处）都传入这次运行结束的状态：成功的地方传 `state.RunStatusDone`，取消传 `Cancelled`，失败传 `Failed`。并删除紧挨着它、针对同一运行的 `SetStatus(terminal)`。`FailRunningDescendants` / `CancelRunningDescendants` 保留，但必须挪到 `finishRun` **之前**。执行器已经设过终态的运行，再设一次会被 §1 的状态机条件挡住，不会改动。

### 6. 两个面启动回收器

- gateway：`pkg/gateway/serve_run.go` 在 `runner.Events = gw.RunEvents()` 之后：`stopReaper := turn.AbandonedRunReaper{Runs: runSvc, Publish: gw.publishGatewayRunEvent, Recover: func(context.Context) { gw.recoverResolvedApprovalWaitsOnce() }}.Start(ctx)`，`defer stopReaper()`。
- TUI：在 `ChatSession` 建好 `Core` 之后启动（`pkg/tui/notify.go` 里构造 `turn.New(...)` 的那个函数），`Publish: s.publishTUIRunEvent`，`Recover` 调 `s.recoverResolvedApprovalWaitsOnce(<当前会话 id>)`；在 `ChatSession` 关闭 `Env` 之前停止（`pkg/tui/chat_session.go` 里调 `s.Env.Close()` 的地方）。
- 两个面原有的"启动时恢复一次"（gateway 的 `recoverResolvedApprovalWaits`、TUI 打开会话时的恢复）保持不变；周期调用依赖的是续跑租约本身的围栏（`ClaimWaitResume` 的 CAS、新鲜租约不可接管），不新增任何规则。

## 缓存影响

无。只改运行与会话的生命周期，不触及任何请求内容。会话独占还有一个间接好处：两个进程不会再同时往同一个会话追加内容，于是这个会话的前缀不会被另一个进程插入的行从中间打断。

## 需要的命令

见 README"常用命令"。本计划额外用到：

| 用途 | 命令 | 成功标志 |
| --- | --- | --- |
| 数据竞争 | `CGO_ENABLED=1 go test -race -tags fts5 ./pkg/state/ -run 'RunOwner\|SessionBusy\|Reap' -count=1` | `ok`，无 `DATA RACE` |

## 范围

**要改的文件**：

- `pkg/state/runrt.go`、`pkg/state/runrt_test.go`、`pkg/state/schema.sql`、`pkg/state/schema_migrations.go`、`pkg/state/schema_migrations_test.go`
- `pkg/turn/events.go`、`pkg/turn/events_test.go`、`pkg/turn/submit.go`、`pkg/turn/submit_test.go`、`pkg/turn/scheduler.go`、`pkg/turn/scheduler_test.go`
- `pkg/run/supervisor.go`，新建 `pkg/run/supervisor_test.go`（基线没有这个文件；测试文件允许新建，名字对应 `supervisor.go`）
- `pkg/process/open.go`、`pkg/process/open_test.go`
- `pkg/gateway/run_control.go`、`pkg/gateway/run_control_test.go`、`pkg/gateway/server.go`、`pkg/gateway/approval.go`、`pkg/gateway/api_extra.go`、`pkg/gateway/serve_run.go`、`pkg/gateway/channels.go`（只在需要时）、对应的 `_test.go`
- `pkg/tui/run.go`、`pkg/tui/notify.go`、`pkg/tui/chat_session.go`、`pkg/tui/chat_turn.go`（`approvalResumeOwner` 的调用方，若需要）及对应 `_test.go`
- `frontend/src/composables/useChatStream.ts`、`frontend/src/lib/providerError.ts`、`frontend/src/locales/index.ts` 及对应测试，以及显示撤回提示的组件（`grep -rn "withdrawnError" frontend/src`）
- 新建 `frontend/e2e/session-busy.spec.ts`
- `docs/plan/SCHEDULED_RUNS_TRANSCRIPT/README.md`（状态行）

**不要碰**：

- 审批续跑租约（`fb_run_waits.resume_*`、`claimWaitResume`、`ListUncertainResolvedWaits`）的规则：本计划只把它的属主身份并入进程属主，不改它的语义。
- `session.Locker` 与 TUI 的 `DispatchSurfaceTurn` 加锁范围：进程内串行照旧，跨进程由数据库规则补上。
- 运行状态的取值集合，以及 `fb_runs` 的其它列。
- `pkg/process/open.go` 里属于 LSP 计划的改动。

## 步骤

### 第 0 步：前置与基线

确认 001 是 DONE，并确认 README"与并行工作的关系"里那组改动已经提交或放弃。运行 README 常用命令里的 Go 全量测试和 `deadcode`，记下基线。

**验证**：全部 `ok`；`deadcode` 只有 `NewRuntimeWithStore`。

### 第 1 步：存储层

按设计 §1 改。测试写进 `pkg/state/runrt_test.go`。两个属主用**同一个库文件**上的两个 `RunStore` 模拟两个进程，过期用 SQL 直接把 `heartbeat_at_ms` 改旧：

1. `TestCreateRunRefusesASessionWithALiveRun`：A 持租并建运行 → B 在同一会话建运行得到 `ErrSessionRunning`（`errors.Is(err, ErrSessionBusy)` 为真，`err.Error()` 等于设计里那句）。在**另一个**会话里 B 可以建。
2. `TestCreateRunRefusesASessionParkedOnApproval`：A 的运行 `SetWaitingAction` 后，B 得到 `ErrSessionAwaitingApproval`。
3. `TestCreateRunIgnoresARunWhoseOwnerIsGone`：A 的心跳改到 `RunOwnerLease` 之前 → B 可以建。A 的旧运行仍是 `running`，等回收。
4. `TestCreateRunIsAtomicAcrossStores`：两个协程各用一个 `RunStore`，同时在同一会话 `CreateRun` 各 50 次（每次成功后立刻置 done）。断言任意时刻会话里活着的主运行不超过 1 个：用一个只记最大值的计数器，在每次 `CreateRun` 成功后、置 done 前查 `SessionHasLiveRun` 加计数。配合 `-race` 跑。
5. `TestReapAbandonedRunsEndsOnlyTheDead`：A 活着、C 已过期、D 的属主行已删除、E 带 `fb_run_waits` 行且属主已过期。回收后只有 C 和 D 的运行变成 `failed`，时钟三列齐全且 `finished >= started`，返回列表就是这两个；再回收一次返回空。
6. `TestRunStatusEndsOnce`：done 的运行不能改回 running、也不能改成 failed；failed 的运行不能改成 done；`waiting_action → running` 会写上调用方的属主。
7. `TestOwnerLeaseRegistersAndLeaves`：`HoldOwnerLease` 之后行存在；`stop` 之后行消失；`Owner == ""` 时返回错误。
8. 迁移：仿照 `TestStateV3UpgradesToV4WithSessionsIntact` 写 `TestStateV4UpgradesToV5WithRunsIntact`。造一个 v4 库：去掉新列、新表、新索引，`user_version = 4`，插一个 `running` 行。打开后版本是 5，那一行 `owner = ''`，第一次 `ReapAbandonedRuns` 就把它收尾。

**验证**：`CGO_ENABLED=1 go test -tags fts5 ./pkg/state/ -count=1` → `ok`；数据竞争命令 → `ok`、无 `DATA RACE`。

### 第 2 步：引擎回收器

按设计 §2 改 `pkg/turn/events.go`。测试写进 `pkg/turn/events_test.go`：一个过期主运行和它的一个过期子运行（子运行先存一条 `subagent_spawned` 事件），`ReapOnce` 后替身 `Publish` 恰好收到：主运行的 `turn_error`（消息为 `AbandonedRunReason`），以及子运行的 `subagent_ended`（字段与 spawned 一致，`Status == "failed"`）。再 `ReapOnce` 一次什么都不发。

**验证**：`CGO_ENABLED=1 go test -tags fts5 ./pkg/turn/ -count=1` → `ok`。

### 第 3 步：进程属主

按设计 §3 改。修好编译器指出的、依赖 `approvalResumeOwner` 内部字段的测试。

**验证**：`CGO_ENABLED=1 go test -tags fts5 ./pkg/process/ ./pkg/gateway/ ./pkg/tui/ -count=1` → `ok`；`grep -rn '"gateway:" + uuid\|"tui:" + uuid' pkg --include='*.go'` → 无输出。

### 第 4 步：入口改为依赖原子建运行

按设计 §4 改。测试：

- `pkg/run/supervisor_test.go`：会话已有活着的主运行时，`run.Run` 返回的错误 `errors.Is(err, state.ErrSessionBusy)`，且 `err.Error()` 就是那一句（没有 `create run:` 前缀）。
- `pkg/gateway/run_control_test.go`：把 001 的 `TestHeartbeatStandsDownWhenTheSessionIsBusy`、`TestHeartbeatStandsDownWhenTheSessionIsParked` 改成用**另一个属主**在库里造活运行或挂起审批（不再靠 `Track` 和闸门替身），断言返回 `state.ErrSessionBusy`、无新行；删除 `TestDetachedTurnRefusedByAnotherRunsApprovalEndsFailed`；自动继续的既有测试原样通过。
- WS 发送路径：会话被另一个属主占着时发消息，收到 `turn_withdrawn`，`Error` 是那一句，没有写入用户行。
- 渠道：同样情况下渠道用户收到的就是那一句。
- TUI（`pkg/tui` 里覆盖 `run.go` 的测试，参照现有 Esc 撤回的测试 `grep -n "withdraw" pkg/tui/*_test.go`）：`Submit` 以 `ErrSessionRunning` 失败后，用户卡片被移除，输入框恢复原文，出现内容为那一句的 `FrameSystem`，没有 `FrameError`。

**验证**：`CGO_ENABLED=1 go test -tags fts5 ./pkg/run/ ./pkg/gateway/ ./pkg/tui/ ./pkg/turn/ -count=1` → `ok`；`grep -rn "ParkedOnApproval\|turn.ErrSessionBusy" pkg --include='*.go'` → 无输出。

### 第 5 步：结束漏斗负责状态

按设计 §5 改。测试写进 `pkg/gateway/run_control_test.go`：

1. 执行器在 `run.Run` 之前就失败（返回一个普通错误，不经过 `run.Run`）。断言页面收到 `turn_error` 的那一刻，库里的状态**已经**是 `failed`：在测试的事件读取循环里，读到 `turn_error` 后立刻 `GetRun` 检查。
2. 同样情况下，紧接着在同一会话里再发起一个无人值守回合（`startDetachedTurn`），能成功。

**验证**：`CGO_ENABLED=1 go test -tags fts5 ./pkg/gateway/ -count=1` → `ok`；`grep -n "SetStatus(.*RunStatus\(Done\|Failed\|Cancelled\))" pkg/gateway/*.go | grep -v _test` 的每一处都不紧跟在同一运行的 `finishRun` 之后（逐处人工核对，结果写进"执行记录"）。

### 第 6 步：启动回收器

按设计 §6 改。在 `pkg/gateway/serve_run.go` 对应测试或 `run_control_test.go` 里加：

1. 库里有一个属主已过期的 `running` 主运行时，gateway 启动回收器后，被绑定的页面收到 `turn_error`（消息为 `AbandonedRunReason`），历史接口里这个运行有 `worked` 行。
2. 一个运行带 `fb_run_waits` 行、`resume_phase = 'execution_started'`、续跑租约已过期：回收器不碰它，但同一个周期里的 `Recover` 把它按"结果不确定"收尾，只发一次 `turn_error`（不是两次）。
3. 一个由**新鲜**租约持有的续跑，周期性的 `Recover` 不会接管它。

**验证**：`CGO_ENABLED=1 go test -tags fts5 ./... -count=1` → 全部 `ok`。

### 第 7 步：真机验证

1. **e2e（假模型）**：新建 `frontend/e2e/session-busy.spec.ts`，环境变量 `E2E_HOME` 已由 `web_e2e.sh` 传给 Playwright。用 `node:child_process` 调 `sqlite3`（库文件用 `find $E2E_HOME -name '*.db'` 定位）：
   - 建一个会话，并在它下面插入一个 `running` 主运行，属主行的心跳设为当前时间。
   - 在网页上对这个会话发消息，断言看到"已在运行"的提示（界面是中文时是中文那一句），消息回到输入框，对话里没有多出用户行；切换到英文后提示变成英文那一句。
   - 再把那个属主的心跳改到很久以前，重新发送，断言能正常得到回复。

   运行 `scripts/acceptance/web_e2e.sh` → `web e2e: PASS`。
2. **崩溃恢复（智谱）**：隔离的 `FOREBRAIN_HOME`，配置照抄 `scripts/acceptance/web_e2e.sh:78-98` 的真模型块。`forebrain gateway start` 后，在网页里发一个需要较长回答的问题（例如"写一篇 1500 字的说明"）。开始流式输出后对 gateway 进程 `kill -9`。重启 gateway，等待不超过 `RunOwnerLease` 加一个回收周期（约 2 分钟）。刷新网页：这个回合以错误块"The process running this turn stopped before it finished."和 "Worked for" 行结尾；`sqlite3` 查到该运行是 `failed`，时钟三列齐全。再用 tmux 跑 `forebrain resume <id>`，截屏：重放里同样有错误和 Worked for 行。
3. **跨进程互斥（智谱）**：同一个 `FOREBRAIN_HOME` 上同时开 gateway 和 tmux 里的 TUI（`forebrain resume <id>`）。在网页里对该会话发一个长回答的问题；趁它在跑，在 TUI 里发一句话。截屏：TUI 显示"已在运行"那一句，输入框里是原文，没有用户卡片残留；网页上的回合不受影响。反过来再做一次：TUI 跑长回答时在网页里发送，网页显示同一句并把消息放回输入框。
4. **心跳与跨进程**：在第 3 步的会话上设 60 秒心跳，TUI 跑一个超过 60 秒的回合。断言这一次心跳被跳过：`fb_heartbeats.last_fired_at` 没有更新，会话里没有心跳行。

**验证**：e2e PASS；四项截屏或查询结果写进下方"执行记录"。

## 测试计划（汇总）

| 层 | 文件 | 用例 |
| --- | --- | --- |
| state | `runrt_test.go`、`schema_migrations_test.go` | 两个属主的独占；挂起审批；过期属主不挡；原子性（`-race`）；回收只收死的、跳过带续跑行的；状态只结束一次；属主登记与离开；v4→v5 |
| turn | `events_test.go`、`scheduler_test.go`、`submit_test.go` | 回收器发的事件；心跳以 `state.ErrSessionBusy` 跳过；删除 `ParkedOnApproval` 的测试 |
| run | `supervisor_test.go`（新建） | 忙的错误原样返回 |
| process | `open_test.go` | 属主登记与离开 |
| gateway | `run_control_test.go` 等 | 心跳/自动继续在别的属主占用时让路；WS 撤回；渠道文案；状态先于报告；启动回收 |
| tui | 覆盖 `run.go` 的测试 | 忙被当作撤回处理 |
| e2e | `session-busy.spec.ts` | 网页撤回与恢复 |

## 完成标准（全部满足）

- [ ] README 常用命令里的每一条都达到成功标志
- [ ] `stateSchemaVersion == 5`，v4→v5 迁移测试通过
- [ ] `grep -rn "ParkedOnApproval\|turn.ErrSessionBusy" pkg --include='*.go'` 无输出
- [ ] `grep -rn "\.CreateRun(" pkg --include='*.go' | grep -v _test` 的每一处，在会话忙时都按设计 §4 处理（逐处核对，写进"执行记录"）
- [ ] 第 1–6 步列出的测试存在且通过，`-race` 无报告
- [ ] e2e PASS；崩溃恢复、跨进程互斥、心跳让路三项真机证据已写进"执行记录"
- [ ] `deadcode` 输出与基线一致
- [ ] `git status` 里只有"范围"列出的文件有改动
- [ ] README 状态行已更新

## STOP 条件

- 001 没有 DONE，或者 README 里那组并行改动还没有处理。
- 某个现有流程需要把一个已结束（done/failed/cancelled）的运行改回 `running`，或者改成另一个终态（`TestRunStatusEndsOnce` 的规则与它冲突）。
- 某个入口需要在同一会话里**同时**存在两个活着的主运行（例如发现某类运行没有 `ParentRunID`，却和父运行同处一个会话）。
- SQLite 驱动不支持 `INSERT ... SELECT ... WHERE NOT EXISTS` 的原子行为，或者第 1 步的原子性测试出现"会话里同时有两个活着的主运行"。
- 回收器和 `ListUncertainResolvedWaits` 的恢复对同一个运行都发出了结束事件（说明"带续跑行就跳过"的边界划错了）。
- 周期性的 `Recover` 让同一个已解决的审批被续跑了两次，或者接管了一个仍由活着的进程持有的续跑。
- 某一步的验证在一次合理修正后仍失败。

## 维护说明

- **新规则**：一个会话同一时刻最多一个活着的主运行，"活着"指停在审批上，或者 running 且属主租约新鲜。这条规则只在 `RunStore.CreateRun` 里实现；任何新的"开始一个回合"的入口都必须经过它，不要再在入口处自己预检。
- **属主**：一个进程一个属主（`RunStore.Owner`），审批续跑也用它。租约 60 秒、每 10 秒续一次；被判为已放弃的运行由任何活着的进程收尾，且只收尾一次。
- **时钟假设**：心跳用的是写入进程的墙钟。同一台机器上没有问题；将来如果多台机器共用一个状态库，需要各机时钟同步（误差远小于租约）。
- **迟到的完成**：一个进程如果被卡住超过租约（比如超过 50 秒拿不到写锁），它的运行可能被别人判为放弃。它之后的终态写入会被"只结束一次"的条件挡住，但它仍可能发出结束事件。这是租约方案固有的窗口，所以租约取得较长；如果真机验证中出现，按 STOP 条件报告。
- 计划 005 用 `SessionHasLiveRun` 判断定时任务上一次触发是否还活着，并依赖回收器发出的 `turn_error` 来收尾崩溃留下的执行记录。

## 执行记录

（执行者在此记录：`CreateRun` 调用点逐处核对结果、`finishRun` 调用点核对结果、崩溃恢复与跨进程互斥的截屏要点、e2e 结果。）
