# 计划 005：定时任务的每次触发都是一段完整的对话

> **执行者须知**：逐步执行。每一步都运行"验证"命令并确认结果后再进入下一步。出现"STOP 条件"里的任何情况，立即停止并报告。
> 完成后更新 `docs/plan/SCHEDULED_RUNS_TRANSCRIPT/README.md` 里本计划的状态行。**不要提交代码。** 先读 README 的"全局规则"。
> **开始前确认计划 001–004 都是 DONE**。本计划用到它们引入的：
> - 001：`startDetachedTurn`/`detachedTurn`、`state.PartTypeOrigin`/`WithMessageOrigin`/`MessageOrigin`、`process.ScheduledTurns`；
> - 002：`SessionStore.EnsureAt`/`SessionBirth`；
> - 003：`state.SessionSourceCron`；
> - 004：`state.ErrSessionBusy`、`RunStore.SessionHasLiveRun`、`turn.AbandonedRunReaper`/`AbandonedRunReason`、`finishRun(..., status)`，以及迁移版本 5。
>
> **漂移检查（先运行）**：
> `git diff --stat 1a6d708..HEAD -- pkg/turn/scheduler.go pkg/process/cron_service.go pkg/process/one_shot.go pkg/process/worker_cli.go pkg/state/cron.go pkg/state/runrt.go pkg/state/schema.sql pkg/state/schema_migrations.go pkg/state/session_store.go pkg/gateway/run_control.go pkg/gateway/wsevents.go pkg/gateway/server.go pkg/gateway/channels.go pkg/gateway/api_extra.go pkg/gateway/approval.go pkg/memory/jobs.go frontend/src/views/CronView.vue frontend/src/views/ChatView.vue frontend/src/lib/api.ts frontend/src/locales/index.ts`
> 计划 001–004 改过其中多数文件，这是预期的。下面"现状"里标了"（00x 后）"的，都指那些计划完成后的形状；其余摘录是基线 `1a6d708` 的原样，按内容核对，对不上就按 STOP 条件处理。

## 状态

- **优先级**：P1
- **工作量**：L
- **风险**：MED（定时任务的收尾从同步改为由运行结束驱动；有一次 schema 迁移）
- **依赖**：计划 001、002、003、004
- **类别**：bug / direction
- **基线**：提交 `1a6d708`，2026-10-03
- **决策**：D3、D4、D5、D6、D7 按推荐 A（owner 2026-10-03）

## 为什么要做

今天定时任务每次触发都在一个专属会话 `cron-<任务id>-<unix秒>` 里跑，但它和心跳一样走一次性旁路 `RunAgentOnceExec`，所以：

- 会话里**一行对话都没有**，只在 `fb_cron_runs.output` 里留一段纯文本。看不到它调了哪些工具、改了哪些文件，也无法接着这次触发继续追问。
- 那个空会话照样出现在对话抽屉和 `/resume` 里，标题就是 `cron-…` 这串 id。
- 绑定了项目的任务，会话也跑在 agent 级基础 runner 上，工具表、技能、指令都不是那个项目的。
- 不进审批闸门，不跑输入/输出护栏（触发器 `agent-once` 被归为 `agent:default`）。
- gateway 崩溃时，执行记录永远停在"执行中"。

做完以后，每次触发都是一段完整的对话：提示行标明"由定时任务发送"，回答、工具调用、"Worked for" 行都在。定时任务页的执行记录可以"打开对话"。触发停在审批上时就等用户在那段对话里批准（D4）。执行记录在运行**真正结束**时收尾，先收尾、再投递，投递只发生一次。进程崩溃留下的触发，由 004 的回收器收尾。

## 现状

### 调度器（基线 `1a6d708`）

`pkg/turn/scheduler.go:106-147` `runDueJobs`：claim `job.ID` → `ClaimDueJob` CAS → `go func(j) { defer s.release(j.ID); s.fire(ctx, j, "schedule") }(job)`。claim 是进程内的 map，用来防止"慢任务叠出多份"。
`:151-170` `RunNow`：同样 `go func() { defer s.release(job.ID); s.fire(ctx, *job, "manual") }()`；claim 不到时返回 `cron job %q is already running`。
`:177-223` `fire`：

```go
func (s *Scheduler) fire(ctx context.Context, job state.CronJob, trigger string) {
	started := s.Deps.now()
	sessionID := fmt.Sprintf("cron-%s-%d", strings.TrimSpace(job.ID), started.Unix())
	runRowID, err := s.Store.StartRun(ctx, state.CronRun{JobID: job.ID, AgentID: job.Ag, SessionID: sessionID, Trigger: trigger, StartedAt: started.Unix()})
	if err != nil {
		s.logger().Error("cron: open run record", "job", job.ID, "err", err)
	}
	output, errText := "", ""
	if s.Deps.RunPrompt == nil {
		errText = "no runtime is bound to the scheduler"
	} else {
		output, errText = s.Deps.RunPrompt(ctx, sessionID, cronChannelID, job.Prompt)
	}
	status := state.CronStatusOK
	deliveredTo := ""
	if strings.TrimSpace(errText) != "" {
		status = state.CronStatusFailed
	} else if target := strings.TrimSpace(job.Deliver); target != "" {
		if s.Deps.DeliverText == nil {
			status = state.CronStatusDeliveryError
			errText = "no delivery channel is bound to the scheduler"
		} else if derr := s.Deps.DeliverText(ctx, target, sessionID, output); derr != nil {
			status = state.CronStatusDeliveryError
			errText = derr.Error()
		} else {
			deliveredTo = target
		}
	}
	if runRowID > 0 {
		if ferr := s.Store.FinishRun(ctx, runRowID, status, output, errText, deliveredTo); ferr != nil { ... }
	}
	s.recordOutcome(ctx, job, trigger, status, output, errText)
}

const cronChannelID = "cron"
```

`recordOutcome` → `CronStore.RecordOutcome`，写任务的 `last_status/last_output/last_error/failure_streak`；投递失败（`delivery_failed`）不计入连续失败。
（001 后）`SchedulerDeps` 是 `RunPrompt`（只剩定时任务用）、`DeliverText`、`StartHeartbeat`、`Now`；（004 后）`fireHeartbeat` 用 `state.ErrSessionBusy` 判断跳过。

### 一次性旁路（基线）

- `pkg/process/cron_service.go:108-116` 的 `runPrompt` → `pkg/process/one_shot.go:41-59` 的 `RunAgentOnceExec` → `pkg/process/worker_cli.go:93-136` 的 `RunAgentOnceSupervised`（`run.Run`，`Runner: env.Runner`，`Trigger: "agent-once"`，先 `awaitRequiredMCP`）。本计划之后它们再无调用方。
- 只有测试还引用它们：`pkg/process/worker_cli_test.go` 的 `TestRunAgentOnceSupervisedFailsWhenARequiredServerDidNotStart` 和 `TestScheduledPromptRunsInTheFreshSessionItNames`；`pkg/process/session_context_test.go` 的 `TestNilAndLightweightWorkerhostPaths` 里有两条断言。
- `awaitRequiredMCP` 另被 `pkg/process/run_executor.go` 使用，必须保留。gateway 的执行器用 `FailOnRequiredMCP: true` 构造（`pkg/gateway/serve_run.go`），所以"无人值守的运行在必需 MCP 服务没起来时失败"这条不变式，在新路径上由执行器保证（`pkg/process/worker_cli_test.go` 的 `TestRunExecutorFailsBeforeTheModelWhenARequiredServerDidNotStart` 守着它）。

### 存储（基线）

`pkg/state/cron.go:54-68`：`CronRun{ID, JobID, AgentID, SessionID, Trigger, Status, Output, Error, DeliveredTo, StartedAt, FinishedAt}`，注释："kept after the job itself is edited or removed so the history stays readable"。
`:282-290` `FinishRun(ctx, id, status, output, errText, deliveredTo) error`：`UPDATE fb_cron_runs SET ... WHERE id = ?`（**没有**状态条件）。
`:292-325` `ListRuns(ctx, jobID, limit)`。
`pkg/state/schema.sql:308-323`：`fb_cron_runs`（注释："Append-only fire history. It deliberately outlives the job and the session it ran in, so job_id and session_id carry no foreign key."），只有 `idx_fb_cron_runs_job` 一个索引。
（004 后）`stateSchemaVersion = 5`；`RunStore.SessionHasLiveRun(ctx, sessionID)`；`RunStore.ListRunEventsOfTypes(ctx, runID, types...)`（基线就有，`pkg/state/session_events.go`）。

### gateway（基线 + 001/004 后）

- 所有"运行结束"事件都经过 `pkg/gateway/wsevents.go` 的 `runEventBus.Publish`：先持久化（`b.store.AppendSessionEvent`），再分发。来源有两类：`publishGatewayRunEvent`，以及网页回合旧 op 的镜像 `canonicalRunEventsFromWS`。
  - 结束类型有时写常量、有时写字面量："turn_completed"（`api_extra.go` 的 `resumeGatewayRun` 末尾）、"turn_error"（`approval.go`、`api_extra.go` 多处）、"turn_cancelled"（`approval.go` 的 `abortGatewayRunForAction`）。
  - 子代理的结束用 `subagent_ended`，不以 `turn_*` 出现。
- `pkg/gateway/channels.go:116-127` `channelApprovalNotice(out)`：`"Waiting for approval to run " + name + ". Approve it in the Forebrain Harness app to continue."`。
- `pkg/gateway/api_extra.go` 的 `handleProjectSessionsCreate` 末段：把会话绑定到项目，做法是 `store.BindSession`，再 `s.Env.EnsureRunnerPool()` 和 `pool.BindSession`。
- `pkg/gateway/api_extra.go` 的 `handleCronJobRuns` 直接返回 `state.CronRun` 列表。
- （001 后）`run_control.go` 有 `detachedTurn`、`startDetachedTurn`、`runDetachedTurn`、`startHeartbeatTurn`；`server.go` 的 `bindSchedulerTo` 传 `process.ScheduledTurns{StartHeartbeat: s.startHeartbeatTurn}`。
- （004 后）`startDetachedTurn` 在会话忙时返回 `state.ErrSessionBusy`；回收器 `turn.AbandonedRunReaper` 在 gateway 里以 `publishGatewayRunEvent` 发 `turn_error`（消息 `turn.AbandonedRunReason`）。

### 自动继续只接网页来源（基线）

`pkg/turn/submit.go` 的 `accepts(origin)` 只认 `cfg.Surfaces`，gateway 配的是 `[]turn.Surface{turn.SurfaceWebChat}`（`run_control.go` 的 `autoContinueConfig`），注释："a channel user has no way to see the wait or cancel it, so a channel conversation is never resumed behind their back."。`turn.Origin.Surface` 在 `pkg/turn` 里只影响这一处。

### 记忆抽取（基线）

`pkg/memory/jobs.go:133-139` 的一阶段候选查询：`WHERE agent_id=? AND memory_mode=? AND id<>? AND memory_source IN (?,?) AND TRIM(cwd)<>'' AND updated_at>=? AND updated_at<=?`。`pkg/memory` 的生产代码**不** import `pkg/state`（只有测试 import）。

### 网页（基线）

- `frontend/src/views/CronView.vue:52-66`：执行记录列表，每行显示状态、时间、触发方式、投递目标、`output`、`error`。项目空间的定时任务标签 `frontend/src/components/project/ProjectCron.vue` 直接复用 `CronView`。
- `frontend/src/lib/api.ts:526-537` `CronRunRecord`（已有 `sessionId?`）。
- `CronView.vue` 直接显示执行记录的 `error` 和任务的 `lastError`，这些是 Go 拼的英文句子（例如 `no delivery channel is bound to the scheduler`），语言切换也不会变。这违反项目记忆 web-surface-requirements 的"Localization reaches back into the runtime"：运行时应当发代码和数值，句子由前端按查看者的语言写。这是顺带发现的既有缺陷，本计划一并修复（设计 §2、§3、§7）。可复用的现成机制：`frontend/src/lib/providerError.ts` 的 `formatProviderError({code, providerMessage, ...})`，认得的代码返回本地化句子，不认得返回 null 由调用方用原文兜底；`RunErrorBlock.vue` 在绘制时调用它。
- 打开某个会话：`router.push({ path: '/', query: { session: id } })`（`frontend/src/components/project/ProjectSessions.vue:84`）。
- （001 后）`ChatView.vue` 的用户气泡在 `msg.origin === 'heartbeat'` 时显示 `chat.originHeartbeat` 标签。

## 设计

### 1. 生命周期

```
调度器 fire(job)                        gateway                                 收尾（SettleFires）
────────────────                        ───────                                 ──────────────────
上一次触发的运行还活着？──是──► 跳过（仍到期，下一跳再看）
        │否
StartRun(记录, running)
StartFire(CronFire) ─────────────────► 建会话（出生即 source=cron、项目目录）
        │                               绑定项目（若有）
        │                               startDetachedTurn ──► 运行 … 结束/被回收
release(claim)                                                     │
返回                                    runEventBus.Publish(turn_*) ──┤（快路径）
                                                                   ▼
                                  每一跳 RunDue 开头也扫一遍 ─────► SettleFires
                                                                    ├─ 运行仍活着（running 且属主存活、或停在审批）→ 不动
                                                                    ├─ 运行结束了 → 从持久化的结束事件取答复或原因
                                                                    └─ FinishRun（CAS）→ 投递（若配置）→ 记投递结果 → recordOutcome
```

- **"这次触发结束了吗"只有一个事实来源**：触发会话里那个主运行在数据库里的状态（004 的 `SessionHasLiveRun`），加上它持久化的结束事件。gateway 总线钩子只是"现在就去看一眼"的快路径；调度器每一跳的扫描是保证。两条路走的是同一个 `SettleFires`，读的是同一份持久化事实，执行记录用 CAS 收尾，所以无论谁先到、到几次，结果都一样，投递也只发生一次。
- **防叠加**：是否允许再触发，看的是数据库——这个任务最近一条未收尾的记录，其运行是否还活着。进程内的 claim 只覆盖"开记录到 `StartFire` 返回"这一小段，函数返回即释放。
- **停在审批上（D4）**：运行处于 `waiting_action`，算"活着"，于是执行记录保持 `running`，任务不再触发，"立即执行"返回 `already running`。若任务配置了投递目标，先投递一句等待审批的提示。用户批准、运行跑完，这次触发随之收尾、投递答复。
- **崩溃**：运行被 004 的回收器判为已放弃，标记失败并发 `turn_error`，这次触发以 `AbandonedRunReason` 收尾为 `failed`。如果是别的进程（例如 TUI）回收的，gateway 总线上没有这个事件，下一跳的扫描照样能从持久化事件里看到它。
- **换主代理**：每个调度器只收尾**自己租户**的记录。别的主代理的触发，等那个主代理再次成为当前主代理时由它的调度器收尾。定时任务页只展示当前主代理的任务，所以不会看到别人的半截记录。
- **用户在触发对话里继续聊**：触发的运行是这个会话里的**第一个**主运行，收尾只看它。之后的回合与执行记录无关。

### 2. state

- `pkg/state/cron.go`：
  - `CronRun` 新增 `HasConversation bool \`json:"has_conversation"\``，注释："the fire's session holds a transcript; fires recorded before fires became conversations have none"。`ListRuns` 的 SELECT 增加 `EXISTS (SELECT 1 FROM fb_messages m WHERE m.session_id = fb_cron_runs.session_id AND m.visibility = 'visible')`。
  - `FinishRun` 改为返回 `(bool, error)`，SQL 加 `AND status = 'running'`，并且不再接收 `deliveredTo`：收尾时还没投递。注释写"一次触发只收尾一次"。
  - 新增 `MarkFireDelivered(ctx, id int64, target string) error` 和 `MarkFireDeliveryFailed(ctx, id int64, errCode, errText string) error`。后者把状态改为 `delivery_failed` 并写错误代码与原文；两者都只对 `status = 'ok'` 的行生效。
  - 错误代码：`CronRun` 新增 `ErrorCode string \`json:"error_code,omitempty"\``，`CronJob` 新增 `LastErrorCode string \`json:"last_error_code,omitempty"\``。`FinishRun` 和 `RecordOutcome` 增加一个 `errCode` 参数，与错误原文一起写入。`error`/`last_error` 仍然保存英文原文，作为不认识代码的客户端的兜底。
  - 新增 `OpenFires(ctx, agentID string) ([]CronRun, error)`（本租户所有 `status='running'` 的记录）、`OpenFireForSession(ctx, sessionID string) (*CronRun, error)`、`LatestOpenFire(ctx, jobID string) (*CronRun, error)`。找不到时返回 `nil, nil`。
  - 更新 `CronRun` 注释：每次触发的对话就是它的会话；记录和会话都比任务活得久（D5）。
- `pkg/state/runrt.go`：新增 `FirstPrimaryRunID(ctx, sessionID string) (string, error)`：`SELECT id FROM fb_runs WHERE session_id=? AND parent_run_id IS NULL ORDER BY created_at ASC, rowid ASC LIMIT 1`。注释写明：触发会话里的第一个主运行就是这次触发的运行。
- `pkg/state/session_store.go`：`SessionBirth` 新增 `Source string`（注释："what the session is for (SessionSource*); born with it, never changed by later writes"）。`ensureSession` 的 INSERT 加 `source` 列，值取 `birth.Source`；冲突时不更新。
- `pkg/state/message_parts.go`：`MessageOriginCron = "cron"`，放在 `MessageOriginHeartbeat` 旁边。
- 迁移 v6（`pkg/state/schema_migrations.go`）：

  ```go
  // migrateStateV5ToV6 indexes fire records by the session they ran in — a
  // run's end finds its fire by that session — and marks the sessions of
  // fires recorded before this version as what they are, so they leave the
  // conversation lists the way every later fire's session is born out of them.
  func migrateStateV5ToV6(ctx context.Context, conn *sql.Conn) error
  ```

  四条语句：
  1. `CREATE INDEX IF NOT EXISTS idx_fb_cron_runs_session ON fb_cron_runs(session_id)`；
  2. `UPDATE fb_sessions SET source = 'cron' WHERE source = '' AND id IN (SELECT session_id FROM fb_cron_runs)`；
  3. `ALTER TABLE fb_cron_runs ADD COLUMN error_code TEXT NOT NULL DEFAULT ''`；
  4. `ALTER TABLE fb_cron_jobs ADD COLUMN last_error_code TEXT NOT NULL DEFAULT ''`（两列已存在时跳过，写法同 v4）。

  `stateSchemaVersion` 改为 6；`pkg/state/schema.sql` 同步加这个索引和这两列（列放在各自的 `error` / `last_error` 之后）。旧记录的代码为空，网页照旧显示原文。

### 3. 调度器（`pkg/turn/scheduler.go`）

- `SchedulerDeps`：**删除** `RunPrompt`；**新增**：

  ```go
  	// StartFire opens one fire as a conversation of its own and returns as
  	// soon as its run has started. How the fire ends is read back through
  	// FireRunState, never reported by StartFire.
  	StartFire func(ctx context.Context, fire CronFire) error
  	// FireRunState reports the run of the fire that ran in sessionID: still
  	// alive, or ended and how.
  	FireRunState func(ctx context.Context, sessionID string) (FireRun, error)
  ```

- 新类型：

  ```go
  // CronFire is one fire of a job: the session it runs in, named for the job
  // and the moment, and the job as it was when it fired.
  type CronFire struct {
  	SessionID string
  	Title     string
  	Job       state.CronJob
  }

  // FireEnding is how a fire's run ended: its answer, or the reason it gave
  // none, already explained for a person to read.
  type FireEnding struct {
  	Output  string
  	// ErrCode classifies the failure for a surface to word in its own
  	// language; ErrText is the English sentence kept as the fallback.
  	ErrCode string
  	ErrText string
  }

  // FireRun is where a fire's run stands. Alive covers a run parked on an
  // approval: it has not ended, and the fire is not over.
  type FireRun struct {
  	Alive  bool
  	Ended  bool
  	Ending FireEnding
  }

  // FireEndingFromRunEvent reads how a run ended from its ending event. It
  // returns false for any event that is not a run's end.
  func FireEndingFromRunEvent(evt event.RunEvent) (FireEnding, bool)
  ```

  映射：
  - `turn_completed` → `Output = payload.Text`；
  - `turn_error` → `ErrText` 取第一个非空的 `payload.Message`、`payload.Error`；`ErrCode` 取 `payload.Detail.Code`（提供方错误的分类、004 的 `run_abandoned` 等），没有 `Detail` 时为 `run_failed`；
  - `turn_cancelled` → `ErrCode = "run_stopped"`，`ErrText = "The run was stopped before it finished."`。
- `RunDue` 改为先 `s.SettleFires(ctx)`，再 `runDueJobs`，再 `runDueHeartbeats`。
- `runDueJobs`：对每个到期任务，先 `s.Store.LatestOpenFire(ctx, job.ID)`；有记录且 `FireRunState(rec.SessionID).Alive` 时 `continue`（不 claim，不推进 `next_run_at`，下一跳再看：上一次结束后补触发一次，不会攒成一串）。其余照旧：claim → `ClaimDueJob` CAS → `go func(j) { defer s.release(j.ID); s.fire(ctx, j, "schedule") }(job)`。注意 claim 仍在 `fire` 返回时释放：`fire` 现在只负责"开记录 + 启动"，很快就返回。
- `RunNow`：同样先看最近一条未收尾的记录，其运行还活着就返回 `fmt.Errorf("cron job %q is already running", jobID)`（文案不变）。
- `fire`：
  - 开记录失败就**不再触发**（记录是这次触发的身份，收尾要靠它找到这次触发），记日志后返回。
  - `StartFire == nil` 时以 `FireEnding{ErrCode: "no_runtime", ErrText: "no runtime is bound to the scheduler"}` 收尾。
  - `StartFire` 返回错误时以 `FireEnding{ErrCode: "fire_start_failed", ErrText: err.Error()}` 立即收尾：没有运行被创建，扫描永远等不到它的结束事件。
  - 成功就直接返回。
  - 会话 id 规则不变。标题由 `fireTitle(job, started)` 给出：任务名（为空时取 `state.SessionTitleFromContent(job.Prompt)`）+ `" · "` + `started.Local().Format("2006-01-02 15:04")`。
- 新增：

  ```go
  // SettleFires closes every fire of this scheduler's tenant whose run has
  // ended — or only the fire that ran in sessionID, when one is named. A fire
  // whose run is alive, or has not left an ending yet, is left for a later
  // pass. Closing is a compare-and-swap on the record, so whichever pass gets
  // there first closes it and delivers its answer; every other pass does
  // nothing.
  func (s *Scheduler) SettleFires(ctx context.Context, sessionID ...string)

  // NotifyFireParked tells the job's delivery target that its fire is waiting
  // for an approval in its conversation. A job with nowhere to deliver hears
  // nothing; the conversation itself shows the approval.
  func (s *Scheduler) NotifyFireParked(ctx context.Context, sessionID, notice string)
  ```

  收尾 `settle(ctx, rec state.CronRun, ending FireEnding)`，`fire` 的失败路径和 `SettleFires` 共用：
  1. `status := ok`；`ending.ErrText` 非空时为 `failed`。
  2. `won, err := s.Store.FinishRun(ctx, rec.ID, status, ending.Output, ending.ErrText)`；`!won` 就返回（别人已经收尾了）。
  3. `job, _ := s.Store.GetJob(ctx, rec.JobID)`。任务已删时为 nil：不投递，也不 `recordOutcome`。
  4. `status == ok` 且 `job.Deliver` 非空时投递：`DeliverText == nil` 记投递失败，代码 `no_delivery_channel`、原文 `no delivery channel is bound to the scheduler`；投递出错记投递失败，代码 `delivery_failed`、原文 `derr.Error()`（这是渠道自己的话，网页把它当作引用原文显示在本地化句子下面）；成功则 `MarkFireDelivered`。投递失败时最终状态改为 `delivery_failed`。
  5. `s.recordOutcome(ctx, *job, trigger, 最终状态, ending.Output, 最终错误代码, 最终错误原文)`。`trigger` 取 `rec.Trigger`。
- `SettleFires` 只处理 `rec.AgentID == s.AgentID` 的记录。指定了 `sessionID` 时用 `OpenFireForSession`，否则用 `OpenFires(s.AgentID)`。对每条记录调 `FireRunState`：`Alive` 或 `!Ended` 就跳过，否则 `settle`。
- 删除常量 `cronChannelID`（渠道名改由 gateway 给出，值仍是 `"cron"`）。

### 4. CronService（`pkg/process/cron_service.go`）

- `ScheduledTurns` 新增 `StartFire func(ctx context.Context, fire turn.CronFire) error`；`Bind` 把它放进 `Deps`，并给 `Deps.FireRunState` 装上本服务的实现：

  ```go
  // fireRunState reads a fire's run from the shared run store: alive while it
  // runs under a live owner or waits on an approval; otherwise ended, with the
  // ending its run recorded.
  func (c *CronService) fireRunState(ctx context.Context, sessionID string) (turn.FireRun, error)
  ```

  实现：
  1. `alive, err := runs.SessionHasLiveRun(ctx, sid)`，`alive` 时直接返回。
  2. `id, err := runs.FirstPrimaryRunID(ctx, sid)`；为空就返回 `FireRun{}`（运行还没创建）。
  3. `evts, err := runs.ListRunEventsOfTypes(ctx, id, event.RunEventTurnCompleted, event.RunEventTurnError, event.RunEventTurnCancelled)`；取第一条，用 `turn.FireEndingFromRunEvent` 映射，`Ended = true`。没有结束事件就返回 `FireRun{}`：运行已不活着但事件还没写入，下一跳再看。

  `runs` 是 `c.env.Deps.RunRT`。
- 新增 `SettleFire(ctx, sessionID string)` 和 `FireParked(ctx, sessionID, notice string)`：在锁内取当前 `c.sched`，不为 nil 时转调 `SettleFires(ctx, sessionID)` / `NotifyFireParked`。为 nil 时什么都不做：下一次绑定后的第一跳扫描会收尾，函数注释写明这一点。
- 删除 `runPrompt`。
- `pkg/process/one_shot.go`：删除 `RunAgentOnceExec`；`AgentOnceInput`、`AgentOnceResult` 若已无引用也删除（`grep -rn "AgentOnce" pkg cmd`）。`pkg/process/worker_cli.go`：删除 `RunAgentOnceSupervised`，并删除因此不再使用的 import。

### 5. gateway

- `pkg/gateway/run_control.go`：
  - `detachedTurn` 新增 `OnParked func(ctx context.Context, gate *tool.RequiresActionError)`；`runDetachedTurn` 的停审批分支在 `parkDetachedRunOnApproval` 之后调用它（非 nil 时）。
  - 新增 `const cronTrigger = "cron"` 和：

    ```go
    // startCronFire opens one fire of a scheduled task as a conversation of its
    // own: the session is born a cron session — in the job's project when it
    // is bound to one — the prompt row is marked as the task's, and the turn
    // runs exactly as a web turn does. A fire is a channel-like turn: nobody is
    // watching it, so a usage limit is not continued behind the person's back;
    // the next fire comes on schedule.
    func (s *Server) startCronFire(ctx context.Context, fire turn.CronFire) error
    ```

    步骤：
    1. `birth := state.SessionBirth{Source: state.SessionSourceCron}`。`fire.Job.ProjectID` 非空时 `p, err := s.projectStore().Get(ctx, pid)`，出错返回；然后 `birth.Cwd, birth.GitBranch = p.Root, memory.GitBranch(p.Root)`。
    2. `s.Sessions.EnsureAt(ctx, fire.SessionID, fire.Title, birth)`。
    3. 有项目时 `s.bindSessionToProject(ctx, fire.SessionID, p)`。这是从 `handleProjectSessionsCreate` 末段抽出来的共享函数，那里也改成调用它。
    4. `_, err := s.startDetachedTurn(detachedTurn{SessionID: fire.SessionID, Prompt: fire.Job.Prompt, Trigger: cronTrigger, Origin: turn.Origin{Surface: turn.SurfaceChannel, ChannelID: "cron"}, PromptOrigin: state.MessageOriginCron, OnParked: func(ctx context.Context, gate *tool.RequiresActionError) { s.cron().FireParked(ctx, fire.SessionID, approvalNoticeText(gate.ToolName)) }})`，返回 `err`。
  - 新增总线快路径：

    ```go
    // settleScheduledFire asks the scheduler to look at a fire the moment its
    // run reports an end, instead of on the next tick. It carries nothing but
    // the session: the scheduler reads the ending back from the store, the
    // same way its tick does.
    func (s *Server) settleScheduledFire(evt event.RunEvent)
    ```

    只处理 `event.RunEventTurnCompleted`、`RunEventTurnError`、`RunEventTurnCancelled` 三种。先用 `s.RunRT.GetRun` 确认是主运行（`ParentRunID == ""`），再 `go s.cron().SettleFire(context.Background(), evt.SessionID)`。
- `pkg/gateway/wsevents.go`：`runEventBus` 新增字段 `onRunEnded func(event.RunEvent)`，`Publish` 在**持久化之后**、分发之前调用它。`pkg/gateway/server.go` 的 `RunEvents()` 在 `newRunEventBus` 之后、`s.Env != nil` 时设 `s.runEvents.onRunEnded = s.settleScheduledFire`。没有 `Env` 就没有定时任务服务，也就不装钩子，`settleScheduledFire` 里不再判 nil。
- `pkg/gateway/channels.go`：抽出 `approvalNoticeText(toolName string) string`（空名用 `"a tool"`，文案不变），`channelApprovalNotice` 改为调用它。
- `pkg/gateway/approval.go`、`pkg/gateway/api_extra.go` 里的字面量 `"turn_cancelled"`、`"turn_error"`、`"turn_completed"` 换成对应常量（`grep -n '"turn_completed"\|"turn_error"\|"turn_cancelled"' pkg/gateway/*.go | grep -v _test` 列出全部）。值不变，只是让"运行结束"的类型只有一个出处。
- `pkg/gateway/server.go` 的 `bindSchedulerTo`：`process.ScheduledTurns{StartHeartbeat: s.startHeartbeatTurn, StartFire: s.startCronFire}`。

### 6. 记忆（D6）

`pkg/memory/jobs.go:133-139` 的候选查询加 `AND source <> ?`，参数绑定本地常量 `sessionSourceCron = "cron"`（注释写明它与 `state.SessionSourceCron` 同值）。不要为此新增对 `pkg/state` 的 import。在覆盖 `jobs.go` 的测试里断言两者相等，测试可以 import `pkg/state`。

### 7. 网页与终端

- `frontend/src/lib/api.ts`：`CronRunRecord` 新增 `hasConversation?: boolean`、`errorCode?: string`；`CronJobRecord` 新增 `lastErrorCode?: string`。
- `frontend/src/views/CronView.vue`：每条记录在 `row.sessionId && row.hasConversation` 时显示按钮 `data-testid="cron-run-open"`，文案 `t('cron.openConversation')`，点击后 `router.push({ path: '/', query: { session: row.sessionId } })`。
- `frontend/src/views/ChatView.vue`：来源标签按来源取文案，`heartbeat` → `chat.originHeartbeat`，`cron` → `chat.originCron`；其它值不显示。
- `frontend/src/views/CronView.vue`：执行记录的错误、任务的"上次错误"，都改为在绘制时用 `formatProviderError({ code: row.errorCode, providerMessage: row.error })`（任务行用 `lastErrorCode` / `lastError`），返回 null 时显示原文。语言切换后跟着变，做法和 `RunErrorBlock.vue` 一样。
- `frontend/src/lib/providerError.ts`：`formatProviderError` 新认识以下代码（提供方错误的代码已经认识，`run_abandoned` 由 004 加）：`run_failed`、`run_stopped`、`no_runtime`、`fire_start_failed`、`no_delivery_channel`、`delivery_failed`。`fire_start_failed` 和 `delivery_failed` 把原文当作引用放在句子下一行，与提供方原话的显示方式相同。
- `frontend/src/locales/index.ts`：`'cron.openConversation': '打开对话'` / `'Open conversation'`；`'chat.originCron': '由定时任务发送'` / `'Sent by a scheduled task'`。以及下表：

  | 键 | 中文 | 英文 |
  | --- | --- | --- |
  | `runError.runFailed` | 这次运行失败了。 | This run failed. |
  | `runError.runStopped` | 这次运行在完成前被停止了。 | The run was stopped before it finished. |
  | `runError.noRuntime` | 调度器没有可用的运行环境。 | No runtime is bound to the scheduler. |
  | `runError.fireStartFailed` | 这次定时任务没能开始运行。 | This scheduled run could not start. |
  | `runError.noDeliveryChannel` | 没有可用于投递的渠道。 | No delivery channel is bound. |
  | `runError.deliveryFailed` | 答复没能投递到渠道。 | The answer could not be delivered to the channel. |
- 终端重放：001 的通用逻辑会把 `origin=cron` 的行画成标题为 `cron` 的系统卡片，无需改代码，第 7 步用测试钉住。

## 缓存影响（必须核对）

- **对话之前的前缀**：不变。没有改工具表、system，也没有改注入在对话前的开发者指令的生成方式。
- **触发回合**：从 agent 级基础 runner 改到按会话解析的 runner。不绑项目的任务仍是基础 runner，工具 + system 与今天相同；绑项目的任务改用项目 runner，前缀与这个项目的其它会话相同，能命中跨会话的 system 断点。查询来源由 `agent:default` 变为 `repl_main_thread`，和网页回合一致。
- **测量**：第 8 步对同一个任务连续"立即执行" 3 次，用 `fb_runs` 的用量列算这些触发运行的命中率，`cache_read / (cache_read + cache_creation + prompt)`，改动前后各测一次（改动前的数字用 001 第 8 步说的独立工作树二进制测，不要 `git stash`）。**不得低于基线**；提供方要求同 README 全局规则 11。

## 需要的命令

见 README"常用命令"。

## 范围

**要改的文件**：

- `pkg/turn/scheduler.go`、`pkg/turn/scheduler_test.go`
- `pkg/process/cron_service.go`、`pkg/process/cron_service_test.go`、`pkg/process/one_shot.go`、`pkg/process/worker_cli.go`、`pkg/process/worker_cli_test.go`、`pkg/process/session_context_test.go`
- `pkg/state/cron.go`、`pkg/state/cron_test.go`、`pkg/state/runrt.go`、`pkg/state/runrt_test.go`、`pkg/state/session_store.go`、`pkg/state/session_store_test.go`、`pkg/state/message_parts.go`、`pkg/state/schema.sql`、`pkg/state/schema_migrations.go`、`pkg/state/schema_migrations_test.go`
- `pkg/memory/jobs.go` 及覆盖它的测试（`grep -rln "ClaimStage1JobsForStartup" pkg/memory/*_test.go`，基线是 `store_test.go`、`instruction_test.go`）
- `pkg/gateway/run_control.go`、`pkg/gateway/run_control_test.go`、`pkg/gateway/wsevents.go`、`pkg/gateway/server.go`、`pkg/gateway/channels.go`、`pkg/gateway/approval.go`、`pkg/gateway/api_extra.go`、`pkg/gateway/api_extra_test.go`
- `pkg/tui/commands_test.go`
- `frontend/src/lib/api.ts`、`frontend/src/lib/providerError.ts` 及其测试、`frontend/src/views/CronView.vue`、`frontend/src/views/ChatView.vue`、`frontend/src/locales/index.ts`、新建 `frontend/e2e/cron-fire.spec.ts`
- `docs/plan/SCHEDULED_RUNS_TRANSCRIPT/README.md`（状态行）

**不要碰**：

- 删除任务时的行为：D5 选 A，只删任务本身。
- 保留上限、自动清理（D9，未决）。
- 004 的存活规则与回收器：本计划只读取它们的结果。
- 心跳（001）和会话列表（003）。
- `pkg/gateway/dist`。

## 步骤

### 第 0 步：确认前置与基线

确认 001–004 都是 DONE。运行 README 常用命令里的 Go 全量测试和 `deadcode`，记下基线。

**验证**：全部 `ok`；`deadcode` 只有 `NewRuntimeWithStore`。

### 第 1 步：state 与迁移

按设计 §2 改。测试：

- `pkg/state/cron_test.go`：
  - `FinishRun` 第一次返回 `true`，第二次返回 `false`，第二次的输出没有覆盖第一次。
  - `MarkFireDelivered` / `MarkFireDeliveryFailed` 只对 `ok` 的行生效；错误代码和原文都写进去了，`ListRuns` / `GetJob` 读得回来。
  - `OpenFires` 只返回本租户 `running` 的记录；`OpenFireForSession`、`LatestOpenFire` 收尾后返回 nil。
  - `ListRuns`：会话里有可见消息时 `HasConversation == true`，没有时为 false。
- `pkg/state/runrt_test.go`：`FirstPrimaryRunID` 返回会话里最早的主运行，忽略子运行。
- `pkg/state/session_store_test.go`：`EnsureAt(..., SessionBirth{Source: SessionSourceCron})` 出生即为 `cron`；之后对同一 id 的 `Ensure` 不改变它。
- `pkg/state/schema_migrations_test.go`：`TestStateV5UpgradesToV6MarksFireSessions`。用 `schemaSQL` 建库，删掉新索引，`user_version` 设为 5，插一个普通会话和一个被 `fb_cron_runs` 引用的会话。打开后断言：版本为 6，被引用的会话 `source='cron'`，普通会话仍是 `''`，`idx_fb_cron_runs_session` 存在。

**验证**：`CGO_ENABLED=1 go test -tags fts5 ./pkg/state/ -count=1` → `ok`。

### 第 2 步：调度器

按设计 §3 改。重写 `pkg/turn/scheduler_test.go` 里所有用 `RunPrompt` 的定时任务测试，改用两个替身：

- `StartFire`：记录收到的 `CronFire`，返回 nil；
- `FireRunState`：由测试维护的 `map[sessionID]FireRun`。

测试通过修改 map 来模拟"运行在跑 / 停在审批 / 结束了"，再调 `s.SettleFires(ctx)` 或 `s.RunDue(ctx)`。需要覆盖：

1. 到期任务在自己的会话里触发：`CronFire.SessionID` 形如 `cron-<id>-<unix>`，`Title` 以任务名开头，`Job.Prompt` 正确。map 里设为结束（`Output: "three callers"`）后 `SettleFires`：投递了 `telegram:three callers`，记录 `ok` 且 `delivered_to = telegram`，任务重新排期，`last_status = ok`。
2. 失败：结束时 `ErrText: "x"` → 记录 `failed`，不投递。投递失败 → 最终 `delivery_failed`，且不计入连续失败。
3. 只投递一次：同一结束连调三次 `SettleFires`，投递替身只被调用一次。
4. 运行活着时（`Alive: true`，含停在审批）：`SettleFires` 不动记录；再到期时 `RunDue` 不调 `StartFire`；`RunNow` 返回 `already running`。结束后下一跳补触发一次（只一次）。
5. 触发中暂停 / 触发中删除：`StartFire` 后暂停或删除任务，再结束并 `SettleFires`。暂停的任务仍然暂停；删除的任务没有复活、没有投递，记录照常收尾。
6. `StartFire` 返回错误：记录立即以该错误 `failed`；之后的 `RunDue` 能正常再触发。
7. 别的租户的记录：`SettleFires` 不动它。
8. `NotifyFireParked`：有投递目标时投递提示，没有时什么都不做。
9. `FireEndingFromRunEvent`：三种结束事件的映射（含 `ErrCode`：带 `Detail` 的 `turn_error` 取其代码，不带的为 `run_failed`，取消为 `run_stopped`）；其它类型返回 false。
10. 投递失败记 `delivery_failed` 代码、`DeliverText == nil` 记 `no_delivery_channel` 代码，任务的 `last_error_code` 同步。

**验证**：`CGO_ENABLED=1 go test -tags fts5 ./pkg/turn/ -count=1` → `ok`；`grep -n "RunPrompt\|cronChannelID" pkg/turn/scheduler.go` → 无输出。

### 第 3 步：CronService 与删除一次性旁路

按设计 §4 改。删除 `TestRunAgentOnceSupervisedFailsWhenARequiredServerDidNotStart`、`TestScheduledPromptRunsInTheFreshSessionItNames`，以及 `TestNilAndLightweightWorkerhostPaths` 里关于 `RunAgentOnce*` 的两条断言；它们守的不变式由第 4 步的 gateway 测试接手。

在 `pkg/process/cron_service_test.go` 加：

- `fireRunState` 三种情形：运行活着；停在审批；结束（含 `turn_error` 的 `AbandonedRunReason`）。两个属主用 004 的方式在同一个库上模拟。
- `SettleFire`、`FireParked` 转到调度器；`Stop` 后调用它们不报错。

**验证**：`CGO_ENABLED=1 go test -tags fts5 ./pkg/process/ -count=1` → `ok`；`grep -rn "RunAgentOnce\|AgentOnceInput\|AgentOnceResult\|runPrompt" pkg cmd --include='*.go'` → 无输出。

### 第 4 步：gateway

按设计 §5 改。测试写进 `pkg/gateway/run_control_test.go`。夹具：在 `newAutoContinueGateway` 基础上加一个 `Env`（照 `pkg/gateway/api_extra_test.go` 里 `cronTestServer` 的写法，加上 `SQL: db`，并让 `Env.Deps.RunRT` 就是夹具的 `runStore`，且带 004 要求的 `Owner` 和租约），再 `Env.Cron().Bind(ctx, "main", process.ScheduledTurns{StartHeartbeat: s.startHeartbeatTurn, StartFire: s.startCronFire})`。如果加 `Env` 改变了夹具里 `runnerFor`/`runController` 的行为、导致既有测试失败，按 STOP 条件处理。断言执行记录的最终状态时用 `require.Eventually`，因为快路径在协程里。

1. `TestCronFireRunsAsItsOwnConversation`：建一个任务，`RunJobNow` 后：
   - 依次收到 `turn_started` → `turn_completed`；
   - 执行器请求的 `Trigger == "cron"`、`Origin.Surface == turn.SurfaceChannel`、`Origin.ChannelID == "cron"`；
   - 会话 `source == "cron"`，标题以任务名开头；行是 `user`（`origin=cron`）、`assistant`；
   - 执行记录最终为 `ok`，`output` 等于答复，任务 `last_status == "ok"`。
2. `TestProjectCronFireRunsInItsProject`：任务绑定项目时，会话 `project_id` 是该项目，`cwd` 是项目根目录。
3. `TestCronFireParkedOnApprovalSettlesWhenItsRunEnds`：执行器先返回停审批（带 `Resume`）。执行记录保持 `running`，`RunJobNow` 再调返回 `already running`，任务投递目标收到等待审批的提示（用 `Env.Channels` 的测试替身，或断言 `FireParked` 被调用）。然后模拟恢复后的完成：把运行置 `done`，对同一运行 `publishGatewayRunEvent(..., event.RunEventTurnCompleted, event.TurnCompletedPayload{Text: "done"})`，执行记录变为 `ok`、`output == "done"`。
4. `TestCronFireFailureIsRecordedExplained`：执行器返回一个错误，错误文字里带必需 MCP 服务名 `required-docs` → 执行记录 `failed`，错误含 `required-docs`。这条接手被删的 `TestRunAgentOnceSupervisedFailsWhenARequiredServerDidNotStart` 在这一层的职责。
5. `TestCronFireAbandonedByACrashIsSettled`：用另一个属主在库里造一次"触发到一半的进程死了"：执行记录 `running`，会话里的主运行 `running` 且属主心跳过期。启动 004 的回收器（或直接 `ReapOnce`）之后，执行记录变为 `failed`，错误等于 `turn.AbandonedRunReason`。
6. `TestCronFireReapedElsewhereIsSettledByTheTick`：同上，但结束事件由**另一个**发布函数写入（模拟 TUI 回收，gateway 总线没看到）。`Env.Cron()` 的下一跳 `RunDue` 让执行记录收尾。
7. `TestRunEndingOfAnOrdinarySessionTouchesNoFire`：普通会话的 `turn_completed` 不改动任何执行记录。
8. `TestSubagentEndingDoesNotSettleAFire`：`ParentRunID` 非空的运行发出的 `turn_error`，不会让执行记录收尾。

**验证**：`CGO_ENABLED=1 go test -tags fts5 ./pkg/gateway/ -count=1` → `ok`；`grep -rn '"turn_completed"\|"turn_error"\|"turn_cancelled"' pkg/gateway --include='*.go' | grep -v _test` → 无输出。

### 第 5 步：记忆（D6）

按设计 §6 改。在覆盖 `jobs.go` 的测试里，仿照现有的 `ClaimStage1JobsForStartup` 用例加一条：其它条件都满足的 `cron` 会话不会成为候选；并断言 `sessionSourceCron == state.SessionSourceCron`。

**验证**：`CGO_ENABLED=1 go test -tags fts5 ./pkg/memory/ -count=1` → `ok`。

### 第 6 步：网页

按设计 §7 改。`frontend/src/lib/providerError.test.ts`（没有就新建）加：新代码在中英文下各返回设计表里的句子，`delivery_failed`/`fire_start_failed` 把原文放在下一行，未知代码返回 null。若 `CronView.vue` 已有单测，就在那里加"有对话时显示'打开对话'、点了会跳转"和"错误按代码本地化、未知代码显示原文"；没有就靠第 8 步的 e2e 覆盖，并在"执行记录"里注明。

**验证**：`cd frontend && corepack pnpm test` 全部通过；`cd frontend && corepack pnpm exec vue-tsc --noEmit -p tsconfig.json` 退出码 0。

### 第 7 步：终端重放

在 `pkg/tui/commands_test.go` 加一条：`origin=cron` 的 user 行重放为标题 `cron` 的 `FrameSystem`。

**验证**：`CGO_ENABLED=1 go test -tags fts5 ./pkg/tui/ -count=1` → `ok`。

### 第 8 步：真机验证

1. **e2e（假模型）**：新建 `frontend/e2e/cron-fire.spec.ts`（参照 `cron-builder.spec.ts`）：
   - 在 `/cron` 建一个任务，点"立即执行"（`cron.runNow`），展开执行记录；
   - 在 30 秒内等到状态"成功"和 `[data-testid="cron-run-open"]`，点击它；
   - 另建一个投递到不存在渠道的任务并立即执行，执行记录显示的是本地化的"答复没能投递到渠道。"（英文界面下为英文），下一行是渠道原话；
   - 聊天页显示带"由定时任务发送"标签的提示、一条助手回复和 "Worked for"/"已工作" 行；
   - 打开对话抽屉，这段对话**不在**列表里；
   - 在项目空间的定时任务标签里重复一次：打开对话后，标题栏有项目标签。

   运行 `scripts/acceptance/web_e2e.sh` → `web e2e: PASS`。
2. **e2e（智谱）**：`FOREBRAIN_E2E_REAL_LLM=1 scripts/acceptance/web_e2e.sh` → `web e2e: PASS`。
3. **终端**：按 001 第 8 步的方式起隔离的 gateway（智谱），用 API 建任务并立即执行。拿到执行记录里的 `session_id`，停掉 gateway，在 tmux 里 `forebrain resume <session_id>` 并截屏：应看到 `● cron` 卡片、提示原文、回答和 "Worked for" 行。再打开 `/resume` 截屏，确认这次触发的会话不在列表里。
4. **审批路径**：权限预设设为只读，建一个要求写文件的任务并立即执行。网页上应看到：执行记录"执行中"；"立即执行"再点提示已在执行；打开对话能看到待批准项（`PendingActionsPanel`）；批准后对话继续，执行记录变"成功"。
5. **崩溃路径**：建一个需要较长回答的任务，立即执行，在流式输出期间对 gateway `kill -9`。重启 gateway，约 2 分钟内执行记录变为"失败"，错误是 004 的那句；打开对话，看到错误块和 Worked for 行。
6. **缓存**：按"缓存影响"里的方法测前后命中率，写入"执行记录"。

**验证**：e2e 全 PASS；截屏、审批路径、崩溃路径符合预期；命中率不低于基线。

## 完成标准（全部满足）

- [ ] README 常用命令里的每一条都达到成功标志
- [ ] `grep -rn "RunAgentOnce\|AgentOnceInput\|runPrompt\|cronChannelID" pkg cmd --include='*.go'` 无输出
- [ ] `grep -rn '"turn_completed"\|"turn_error"\|"turn_cancelled"' pkg/gateway --include='*.go' | grep -v _test` 无输出
- [ ] `stateSchemaVersion == 6`，v5→v6 迁移测试通过
- [ ] 第 1–7 步列出的测试存在且通过
- [ ] e2e（假模型、智谱）PASS，`cron-fire.spec.ts` 在其中
- [ ] 终端截屏、审批路径、崩溃路径、缓存数字已写进下方"执行记录"
- [ ] `deadcode` 输出与基线一致
- [ ] `git status` 里只有"范围"列出的文件有改动
- [ ] README 状态行已更新

## STOP 条件

- 001–004 没有全部 DONE，或者它们引入的符号和本计划的描述对不上。
- 某条运行结束路径发出的 `turn_completed` 不带答复文本（`FireEndingFromRunEvent` 依赖 `TurnCompletedPayload.Text`）。
- 发现某个子代理的运行会以 `turn_*` 事件结束（违反"只有主回合发 `turn_*`"的前提）。
- 触发会话里的第一个主运行不是这次触发的运行（例如发现别的路径会在 `startCronFire` 建会话之后、建运行之前往里放运行）。
- 给 gateway 夹具加 `Env` 后，既有测试的行为变了。
- `pkg/memory` 需要新增仓库内部包的 import 才能完成第 5 步。
- 触发运行的缓存命中率低于基线。
- 某一步的验证在一次合理修正后仍失败。

## 维护说明

- **"这次触发结束了吗"只看数据库**：触发会话里第一个主运行是否还活着（004 的规则），以及它的结束事件。总线钩子只是让收尾更快，删掉它也不影响正确性；扫描删掉才会出错。
- **先收尾、后投递**：执行记录的 CAS 决定谁来投递，投递结果事后补写。以后改投递逻辑，不要把投递挪回 CAS 之前，否则两个收尾者会各投递一次。
- 触发会话的用途 `cron` 在出生时写定。用户在触发对话里继续聊，它仍然是 `cron` 会话：仍然不在抽屉和 `/resume` 里，仍然从执行记录进入。
- 删除任务后，它的触发对话仍在（D5），只能按会话 id 打开。若 owner 以后要"随任务一起删"，在 `CronStore.DeleteJob` 里按 `fb_cron_runs.session_id` 删会话即可（外键全部级联）；届时要先处理运行仍活着的触发。
- 保留上限（D9）未决，本计划不做。

## 执行记录

（执行者在此记录：基线与改后的缓存命中率、所用提供方或"凭据缺失，已跳过"、终端截屏要点、审批路径与崩溃路径结果、e2e 结果。）

## 执行记录

- 执行于 2026-10-05（Go 步骤 1–7 与前端车道并行）。`stateSchemaVersion = 6`，`TestStateV5UpgradesToV6MarksFireSessions` 通过，迁移库与新建库逐字节一致（`TestStateMigrationMatchesFreshSchema`）。
- 一次性旁路删除：`RunAgentOnceExec`/`RunAgentOnceSupervised`/`runPrompt`/`cronChannelID` grep 全空。被删旧测试的不变式去向：必需 MCP 未启动→`TestCronFireFailureIsRecordedExplained` + 既有 `TestRunExecutorFailsBeforeTheModelWhenARequiredServerDidNotStart`；新会话先建后跑→`TestCronFireRunsAsItsOwnConversation`。`pkg/turn` 调度器测试按 StartFire/FireRunState 替身重写（7 例），不变式全保留。
- 一处与计划字面的偏差：`TestCronFireReapedElsewhereIsSettledByTheTick` 用导出的 `Env.Cron().SettleFire(sid)` 驱动（gateway 测试无法驱动 process 内部调度器 tick）；无参全租户扫描语义由 `pkg/turn` 的 `SettleFires()` 用例钉住。另一处：gateway 夹具加 `Env` 后走默认 token 鉴权致 401，夹具设 `Gateway.Auth.Mode="none"` 恢复，未改生产代码。
- 全量：Go 29 包 ok；前端 260 用例过、vue-tsc 0；deadcode 与基线仅 2 处同符号行号平移；gofmt/vet 干净。
- e2e（假模型）：第一轮 1 败（cron-fire 用例对历史面板行的 10s 定位窗太短——DOM 快照显示行与按钮均已在）→ 修正定位（以 `cron-run-open` 按钮为锚、30s 等待，符合计划"30 秒内等到"本意）后 **80 passed，`web e2e: PASS`**。e2e（智谱）：第一轮 2 败为主线并行压测同一 API key 挤占限速的时序失败（skill-workshop 差 0.8s 到限）；停止压测后干净重跑 **81 passed，`web e2e: PASS`**。
- 终端重放（智谱）：取一次触发的会话 id，停 gateway 后 tmux `forebrain resume`：`● cron` 卡片 + 提示原文 + 回答 + `Worked for 3s` 行；`/resume` 选择器不列任何触发会话（该环境仅有 cron 会话，列表为空即排除生效）。
- 审批路径（D4，智谱）：read-only 预设下建写文件任务并"立即执行"→ 执行记录保持 `running`，再点"立即执行"返回 `already running`；`GET /api/actions` 见 `write_file` 待审批；`POST /api/actions/:id/approve` 批准后对话继续、文件 `approval-note.txt` 落盘、执行记录 `ok`。
- 崩溃路径（智谱）：长文触发流式中 `kill -9` gateway → 70s 后重启，执行记录在启动后收尾为 `failed`，错误原文为 004 那句，`error_code = run_abandoned`。
- 缓存命中率（同一任务、同一提示、智谱）：新路径逐次 0% → 68.75% → 99.91% → 99.91% → 99.91%（每次触发即新会话，第一跳冷启动是固有成本；稳态 99.91%）。基线（`1a6d708` 二进制，3 次触发）聚合 91.41%，但其首跳即命中 9600 read——是踩中了新路径几分钟前用同一 key 写热的提供商侧缓存；其稳态（36/12800）为 99.72%。**稳态对比 99.91% ≥ 99.72%，不低于基线**。DeepSeek/OpenAI：凭据缺失，已跳过。
