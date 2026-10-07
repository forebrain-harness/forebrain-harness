# 计划 001：心跳成为它所在对话里的真实回合

> **执行者须知**：逐步执行。每一步都运行"验证"命令并确认结果后再进入下一步。出现"STOP 条件"里的任何情况，立即停止并报告，不要自行发挥。
> 完成后更新 `docs/plan/SCHEDULED_RUNS_TRANSCRIPT/README.md` 里本计划的状态行。**不要提交代码。** 先读 README 的"全局规则"，它们对本计划全部适用。
>
> **漂移检查（先运行）**：
> `git diff --stat 1a6d708..HEAD -- pkg/turn/scheduler.go pkg/turn/session.go pkg/turn/submit.go pkg/process/cron_service.go pkg/gateway/run_control.go pkg/gateway/server.go pkg/gateway/agents_api.go pkg/gateway/api_extra.go pkg/state/message_parts.go pkg/event/run_events.go pkg/tui/commands.go pkg/tui/reducer.go frontend/src/composables/useChatStream.ts frontend/src/lib/api.ts frontend/src/lib/forebrainGatewayRuntime.ts frontend/src/views/ChatView.vue frontend/src/locales/index.ts`
> 有输出时，逐个对照下面"现状"里的摘录核对；对不上就按 STOP 条件处理。

## 状态

- **优先级**：P1（owner 指定第一个做）
- **工作量**：M
- **风险**：MED（改动了自动继续共用的 gateway 无人值守回合路径；靠自动继续的既有测试兜住）
- **依赖**：无
- **类别**：bug
- **基线**：提交 `1a6d708`，2026-10-03
- **决策**：D1、D2 按推荐 A（owner 2026-10-03）

## 为什么要做

心跳是用户给某个对话设的一条周期性指令："这个对话空闲时，每隔 N 分钟把这句话再发一次"。今天它触发时会真的调用模型，但模型的回答**被直接丢弃**。提示和回答都不写进对话记录，页面上什么也看不到，下一个回合的上下文里也没有它。
它还走错了 runner：绑定项目的会话也跑在 agent 级基础 runner 上（工具表、技能、指令都不对，提示缓存也因此对不上）。它绕过了"会话停在审批上时不得开新回合"的闸门，不受运行控制器追踪（用户停不掉它，网页上的新回合可能和它并发），也不跑输入/输出护栏。
做完以后，心跳触发和用户在网页上发一条消息走同一条路：提示落库并标明"由心跳发送"，回答流式推送到正在看这个对话的页面，有 "Worked for" 收尾行，网页刷新和终端 `/resume` 重放都完整。

## 现状（基线 `1a6d708` 的事实）

### 调度器：心跳的答复被丢弃

`pkg/turn/scheduler.go:23-37`，调度器依赖：

```go
type SchedulerDeps struct {
	RunPrompt func(ctx context.Context, sessionID, channelID, prompt string) (output string, errText string)
	DeliverText func(ctx context.Context, channelID, sessionID, text string) error
	SessionBusy func(sessionID string) bool
	Now func() time.Time
}
```

`pkg/turn/scheduler.go:268-311`，心跳的"忙则跳过"和触发：

```go
	for _, hb := range beats {
		if hb.NextRunAt == nil {
			continue
		}
		if s.Deps.SessionBusy != nil && s.Deps.SessionBusy(hb.SessionID) {
			s.reanchor(ctx, hb, now, false)
			continue
		}
		if !s.claim("hb:" + hb.SessionID) {
			continue
		}
		next := now.Add(heartbeatInterval(hb)).Unix()
		won, claimErr := s.Store.ClaimHeartbeat(ctx, hb.SessionID, *hb.NextRunAt, next)
		...
		go func(beat state.Heartbeat) {
			defer s.release("hb:" + beat.SessionID)
			s.fireHeartbeat(ctx, beat)
		}(hb)
	}
}

func (s *Scheduler) fireHeartbeat(ctx context.Context, hb state.Heartbeat) {
	if s.Deps.RunPrompt == nil {
		return
	}
	if _, errText := s.Deps.RunPrompt(ctx, hb.SessionID, heartbeatChannelID, hb.Prompt); errText != "" {
		s.logger().Error("heartbeat: run", "session", hb.SessionID, "err", errText)
	}
	s.reanchor(ctx, hb, s.Deps.now(), true)
}
```

`reanchor(ctx, hb, now, fired)`（`:329-339`）把下次触发设为 `now + 间隔`；`fired=true` 时同时记下 `last_fired_at`。

### RunPrompt 是一次性旁路

`pkg/process/cron_service.go:54-80`，`Bind` 构造调度器：

```go
func (c *CronService) Bind(ctx context.Context, agentID string) {
	...
	next := &turn.Scheduler{
		Store:   c.store(),
		AgentID: id,
		Deps: turn.SchedulerDeps{
			RunPrompt:   c.runPrompt,
			DeliverText: c.deliver,
			SessionBusy: c.sessionBusy,
		},
	}
```

`:111-116` 的 `runPrompt` 调 `c.env.RunAgentOnceExec(...)`（`pkg/process/one_shot.go:41`），后者调 `RunAgentOnceSupervised`（`pkg/process/worker_cli.go:93-136`）：

```go
	hc := hook.HookContext{SessionID: sid, Channel: ch, Trigger: "agent-once"}
	opts := run.Options{
		RunRT:        env.Deps.RunRT,
		Runner:       env.Runner,          // agent 级基础 runner，项目会话也用它
		...
	}
	return run.Run(opts)
```

这条路不写 `fb_messages`，不发运行事件，不经过 `turn.Service.Submit`（因此不经过审批闸门和会话锁），也不在 `run.Controller` 里登记。触发器 `"agent-once"` 被 `pkg/run/supervisor.go:338-371` 的 `querySourceForRun` 归为 `"agent:default"`，而 `explicitMainThreadQuerySource`（`pkg/run/typedsubagent_llm.go:83`）对它返回 false，所以 `pkg/run/llm_middleware.go:415-447` 的输入/输出护栏在心跳回合上**不生效**。

`:131-137` 的 `sessionBusy` 只问进程内的 `env.Control.Find`。gateway 重启后，停在审批上的运行只在数据库里，它看不到。

**只有 gateway 绑定调度器**：`pkg/gateway/server.go:122-127` 的 `bindSchedulerTo` 调 `s.Env.Cron().Bind(ctx, id)`，调用点是 `server.go:581` 和 `agents_api.go:158`。终端不跑调度器，也没有心跳界面。

### 可以复用的现成路径：自动继续

`pkg/gateway/run_control.go:586-624` 的 `continueAfterUsageLimit` 和 `:627-727` 的 `runAutoContinuation`，就是"没有页面在背后的网页回合"的完整实现：

- 会话忙（`s.runController().Find(sid)`）就拒绝。
- `s.RunRT.CreateRun` 建运行行，`runController().Track` 登记（页面的停止按钮、插话、排队都能找到它），然后 `go` 出去。
- 协程里依次：发 `turn_started`，`turn.PersistUserTurn` 写提示行（带 `RunID`），装好流式输出、部分会话捕获、`process.AgentContextForProject`、`withDetachedGatewayApprovalHooks`，再 `s.Core.Submit(...)`。
- 结束分支：停在审批上 → `parkDetachedRunOnApproval`；取消 → `persistCancelledGatewayTurn` 加 `turn_cancelled`；失败 → 同样持久化部分内容，再发 `turn_error`（`llm.ExplainError`）；成功 → `finishSuccessfulTurn`（写回答并盖上运行时钟），再发 `turn_completed`。

它的入参是写死的：`Trigger: autoContinueTrigger`（`"auto_continue"`），`Origin: turn.Origin{Surface: turn.SurfaceWebChat, ChannelID: "webchat"}`。

`:734-757` 的 `parkDetachedRunOnApproval` 用的是**不绑定运行**的 `s.Sessions.AppendMessageSequence(ctx, sid, gate.SessionSnapshot, "", "")`。网页自己的回合在同样的地方用的是 `AppendMessageSequenceForRun(agCtx, sid, runID, rae.SessionSnapshot, "", "")`（`pkg/gateway/server.go:1923-1931`）。前者写出的行没有 `run_id`，重放时无法归到运行名下。这是同类缺陷，本计划一并修复。

`turn.Service.Submit`（`pkg/turn/submit.go:70-160`）在会话有挂起审批时直接拒绝，返回 `TurnOutcome{Status: TurnWaitingApproval, Approval: pending}`，**`Resume` 为 nil，`RunID` 是那个挂起的旧运行**。`runAutoContinuation` 只处理 `Resume != nil` 的情况，对这种拒绝会走到"成功"分支，写一个空回答。今天自动继续碰不到它，因为进程内停在审批的运行会让 `Find` 判忙，而自动继续的等待也不跨重启；但心跳会跨重启，必须正确处理。gateway 的审批闸门在 `pkg/gateway/serve_run.go:79-83` 装进 `turn.New(... turn.WithApprovalGate(...))`，存在 `Service` 的私有字段 `approval` 里（`submit.go:77-85` 用它）。

### 消息行与来源

`pkg/turn/session.go:240-304`，`UserTurn` 和 `PersistUserTurn`：先用 `state.MessagePartsJSON(llm.UserMessage(...), modelInput)` 得到 `partsJSON`，再调 `store.AppendStructuredMessageForRun(ctx, sid, runID, "user", displayContent, "", partsJSON, ...)`。

`pkg/state/message_parts.go:10-22` 定义了 parts 类型，其中有两个只用于展示的先例：`PartTypeAttachment`（"display-only: it adds nothing to what the model is sent"）和 `PartTypeIsMeta`。`ParseMessageParts`（`:317` 起）对不认识的 `type` 一律忽略，所以新增一个只用于展示的 part 不会改变发给模型的内容。`pkg/run/transcript.go:170-190` 判断"运行开始前已存的那条用户消息"时比较的也是**解析后的文本**，同样不受影响。

### 历史接口、网页、终端重放

- `pkg/gateway/api_extra.go:1202-1222`：`handleChatMessages` 的行结构 `msg`（含 `Attachments`、`MemoryCitation`）。`:1279-1300` 逐行组装，附件由 `state.MessageAttachments(t.PartsJSON)` 读出。
- `frontend/src/composables/useChatStream.ts:468-480`：`conversationFromTranscript` 把 `role === 'user'` 的行变成 `ChatMessage`。`:1682-1707` 的 `applyAutoContinueEvent` 是"运行时替用户发出的消息"在**实时**流里出现的先例（只在 `!historical && sequence > observerHighWater` 时追加一条 user 消息）。`:1712` 和 `:3052` 两处在投影进运行时间线之前把这类事件拦下。
- `frontend/src/lib/forebrainGatewayRuntime.ts:43-45`：运行事件类型的联合类型。
- `frontend/src/views/ChatView.vue:214-215`：用户气泡 `<div v-else-if="msg.role === 'user' && String(msg.content ?? '').trim()" class="whitespace-pre-wrap break-words">{{ msg.content }}</div>`，它在一条 `v-if / v-else-if` 链里。
- `pkg/tui/commands.go:156-215`：`replayTurnWithReducer` 重放存储行，用户行最后走 `renderReplayFrames(renderer, reducer, Message{Kind: kind, Content: content, ...}, "", meta, timing)`。
- `pkg/tui/reducer.go:1426-1434`：`MsgKindError` 的标题取 `firstNonEmpty(msg.Title, "error")`，而默认分支（`MsgKindSystem`）把标题写死成 `"system"`。

### 要遵守的约定（照抄风格）

- gateway 无人值守回合的写法：以 `run_control.go:627-727` 为范本，函数注释写"为什么"。
- 测试：gateway 用 `pkg/gateway/run_control_test.go:792-821` 的 `newAutoContinueGateway` 夹具和 `:930-961` 的 `TestAutoContinuationRunsAsADetachedWebTurn` 的断言方式；调度器用 `pkg/turn/scheduler_test.go:200-251` 的 `TestHeartbeatFiresIntoItsSessionAndYieldsToARunningTurn`；前端用 `frontend/src/composables/useChatStream.test.ts:2304-2380` 的 `describe('auto-continue after a usage limit')`。

## 设计

### 1. "消息来源"标记（state，只用于展示）

在 `pkg/state/message_parts.go` 新增：

```go
	// PartTypeOrigin names who wrote a user message on the person's behalf —
	// a heartbeat, a scheduled task. It is display-only: ParseMessageParts
	// ignores it, so the model is sent exactly what the same text typed by
	// hand would send, and the cached prefix is unchanged.
	PartTypeOrigin = "origin"
```

```go
// Who wrote a user message on the person's behalf, carried by PartTypeOrigin.
const (
	MessageOriginHeartbeat = "heartbeat"
)

// WithMessageOrigin returns partsJSON with the origin part appended.
func WithMessageOrigin(partsJSON, origin string) string

// MessageOrigin reads the origin part back; "" for a message the person wrote.
func MessageOrigin(partsJSON string) string
```

存储形状：在 parts 数组末尾追加 `{"type":"origin","kind":"heartbeat"}`。`partsJSON` 解析失败时 `WithMessageOrigin` 返回原值，因为这个值来自本进程刚生成的 JSON，不会出现这种情况；**不要**为此加兜底分支，直接用 `json.Unmarshal` 的错误返回原串即可。

### 2. 引擎层

- `pkg/turn/session.go`：`UserTurn` 新增字段 `Origin string`（注释："who wrote this message on the person's behalf; empty when the person did"）。`PersistUserTurn` 在得到 `partsJSON` 之后、写库之前，`Origin` 非空时执行 `partsJSON = state.WithMessageOrigin(partsJSON, in.Origin)`。
- `pkg/turn/submit.go`：在 `Submit` 旁边新增

  ```go
  // ParkedOnApproval reports whether the session has a run parked on an
  // approval — the condition Submit refuses a new turn for. A surface that
  // starts a turn nobody asked for right now asks first, so a turn that
  // cannot start leaves no run and no row behind.
  func (s *Service) ParkedOnApproval(ctx context.Context, sessionID string) (bool, error)
  ```

  实现：`s == nil || s.approval == nil` 返回 `false, nil`；否则 `pending, err := s.approval.Pending(ctx, sessionID)`，返回 `pending != nil, err`。
- `pkg/turn/scheduler.go`：
  - 新增 `var ErrSessionBusy = errors.New("turn: the session has a turn in flight or waiting for approval")`，注释说明：心跳的启动方用它表示"这次不触发，不是故障"。
  - `SchedulerDeps` **删除** `SessionBusy`，**新增** `StartHeartbeat func(ctx context.Context, sessionID, prompt string) error`。注释：它把心跳作为该会话的一个回合启动，回合开始后立即返回；会话忙或停在审批上时返回包装了 `ErrSessionBusy` 的错误。`RunPrompt` 在本计划里只剩定时任务使用（计划 005 会删掉它），把它的注释改成只描述定时任务。
  - `runDueHeartbeats` 删掉 `SessionBusy` 分支，其余（claim、`ClaimHeartbeat` CAS、协程）不变。"忙"改由启动方统一判断（单一事实来源，和自动继续同一条规则）。
  - `fireHeartbeat` 改成：

    ```go
    func (s *Scheduler) fireHeartbeat(ctx context.Context, hb state.Heartbeat) {
    	if s.Deps.StartHeartbeat == nil {
    		s.logger().Error("heartbeat: no runtime is bound to the scheduler", "session", hb.SessionID)
    		s.reanchor(ctx, hb, s.Deps.now(), false)
    		return
    	}
    	err := s.Deps.StartHeartbeat(ctx, hb.SessionID, hb.Prompt)
    	switch {
    	case err == nil:
    		s.reanchor(ctx, hb, s.Deps.now(), true)
    	case errors.Is(err, ErrSessionBusy):
    		// A heartbeat never interrupts a turn: this beat is skipped, and the
    		// next one comes an interval from now rather than piling up.
    		s.reanchor(ctx, hb, s.Deps.now(), false)
    	default:
    		s.logger().Error("heartbeat: start", "session", hb.SessionID, "err", err)
    		s.reanchor(ctx, hb, s.Deps.now(), false)
    	}
    }
    ```

  - 更新 `runDueHeartbeats` 上方的文档注释：说明"忙"由启动方判断，间隔从本次心跳**开始**起算（决策 D2）。
  - 删除不再使用的常量 `heartbeatChannelID`（`:313`）。渠道名改由 gateway 在 `Origin.ChannelID` 里给出，值仍是 `"heartbeat"`。
- `pkg/event/run_events.go`：新增 `RunEventHeartbeatFired = "heartbeat_fired"`，放在自动继续那组常量后面，加一行注释；再新增

  ```go
  // HeartbeatFiredPayload marks a heartbeat starting a turn in its
  // conversation. Prompt is the message it sent, so a page watching a run it
  // did not start can draw that message as the turn's first row.
  type HeartbeatFiredPayload struct {
  	Prompt string `json:"prompt,omitempty"`
  }
  ```

### 3. gateway：把自动继续的启动器一般化

在 `pkg/gateway/run_control.go`（**不要新建文件**）：

```go
// detachedTurn is a turn the runtime starts with no page behind it: a
// continuation after a usage limit, a heartbeat. Every page observing the
// session watches it through the event log; nothing owns it but the run.
type detachedTurn struct {
	SessionID string
	Prompt    string
	// Trigger names the turn to hooks and telemetry.
	Trigger string
	// Origin is where the turn comes from. Its surface decides whether a
	// usage limit hit by this turn is continued by itself later.
	Origin turn.Origin
	// PromptOrigin marks the prompt row as written on the person's behalf
	// (state.MessageOrigin*); empty when the prompt is the runtime's own text.
	PromptOrigin string
	// Announce publishes, under the new run and before turn_started, what
	// began it. Nil when the engine has announced it already.
	Announce func(ctx context.Context, runID string)
}
```

- 新增 `func (s *Server) startDetachedTurn(t detachedTurn) (string, error)`：搬入 `continueAfterUsageLimit` 现有的同步部分（`:589-623`）。忙判断扩成两条，任一成立即返回 `turn.ErrSessionBusy`：
  1. `s.runController().Find(sid)` 找到运行；
  2. `s.Core.ParkedOnApproval(ctx, sid)` 为 true（这一条返回 error 时原样返回该 error）。

  其余照搬：建运行、`runStartedAt.Store`、`Track`、排队钩子，然后 `go s.runDetachedTurn(...)`，返回 `runID`。
- `continueAfterUsageLimit` 只保留参数检查，然后调用 `startDetachedTurn(detachedTurn{SessionID: plan.SessionID, Prompt: prompt, Trigger: autoContinueTrigger, Origin: turn.Origin{Surface: turn.SurfaceWebChat, ChannelID: "webchat"}})`。`errors.Is(err, turn.ErrSessionBusy)` 时返回 `turn.ErrAutoContinueUnavailable`，其它错误原样返回（与今天行为一致）。
- `runAutoContinuation` 改名为 `runDetachedTurn(runCtx context.Context, t detachedTurn, runID string, startedAt time.Time, inputRT *run.TurnInputRuntime)`，改动只有以下几处：
  - 发 `turn_started` 之前：`if t.Announce != nil { t.Announce(ctx, runID) }`。
  - `turn.PersistUserTurn(..., turn.UserTurn{SessionID: sid, RunID: runID, ModelInput: t.Prompt, RawInput: t.Prompt, Origin: t.PromptOrigin})`。
  - `TurnRequest` 用 `t.Origin`、`t.Trigger`、`t.Prompt`。
  - `slog.Error("auto-continue turn failed", ...)` 改成 `slog.Error("detached turn failed", "trigger", t.Trigger, ...)`。
  - **新增一个结束分支**，紧跟在已有的 `TurnWaitingApproval && Resume != nil` 转换之后、`switch` 之前：

    ```go
    	if outcome.Status == turn.TurnWaitingApproval && outcome.Resume == nil {
    		// Submit refused: another run in this session is parked on an
    		// approval. This run never ran; it ends here, failed, with a line
    		// that says why.
    		...标记失败、finishRun、发 turn_error（Message 用下面这句）...
    		return
    	}
    ```

    `turn_error` 的 `Message`/`Error` 是英文兜底句 `This conversation is waiting for an approval, so this turn did not run.`，同时带 `Detail: &event.TurnErrorDetail{Code: "session_awaiting_approval"}`。网页按代码在绘制时用自己的语言写这句话（见 §5），不直接显示 Go 拼的句子（项目记忆 web-surface-requirements："Localization reaches back into the runtime"）。
- `parkDetachedRunOnApproval`：把 `s.Sessions.AppendMessageSequence(ctx, sid, gate.SessionSnapshot, "", "")` 换成 `s.Sessions.AppendMessageSequenceForRun(ctx, sid, runID, gate.SessionSnapshot, "", "")`。
- 新增心跳启动器（同一文件）：

  ```go
  // heartbeatTrigger names a heartbeat's turn to hooks and telemetry.
  const heartbeatTrigger = "heartbeat"

  // startHeartbeatTurn starts a session's heartbeat as a turn of that
  // conversation, exactly as if a page had sent the prompt: the prompt row is
  // marked as the heartbeat's, the answer streams to every page watching, and
  // the run ends with its worked line. It returns once the run has started.
  func (s *Server) startHeartbeatTurn(_ context.Context, sessionID, prompt string) error {
  	_, err := s.startDetachedTurn(detachedTurn{
  		SessionID:    sessionID,
  		Prompt:       prompt,
  		Trigger:      heartbeatTrigger,
  		Origin:       turn.Origin{Surface: turn.SurfaceWebChat, ChannelID: "heartbeat"},
  		PromptOrigin: state.MessageOriginHeartbeat,
  		Announce: func(ctx context.Context, runID string) {
  			_ = s.publishGatewayRunEvent(ctx, sessionID, runID, event.RunEventHeartbeatFired, event.HeartbeatFiredPayload{Prompt: prompt})
  		},
  	})
  	return err
  }
  ```

  `ChannelID: "heartbeat"` 沿用今天的 `heartbeatChannelID`，钩子看到的渠道名不变。`s == nil || s.Core == nil` 时返回 `fmt.Errorf("heartbeat: no turn runtime")`，和 `continueAfterUsageLimit` 的参数检查放在同样的位置。

### 4. 调度器接线

- `pkg/process/cron_service.go`：

  ```go
  // ScheduledTurns is how the surface hosting the scheduler runs scheduled
  // work as turns of a conversation. Only the gateway hosts one.
  type ScheduledTurns struct {
  	StartHeartbeat func(ctx context.Context, sessionID, prompt string) error
  }
  ```

  `Bind(ctx context.Context, agentID string, turns ScheduledTurns)`：`Deps` 里去掉 `SessionBusy`，加上 `StartHeartbeat: turns.StartHeartbeat`。删除 `sessionBusy` 方法。更新 `Bind` 的文档注释。
- `pkg/gateway/server.go:122-127`：`s.Env.Cron().Bind(ctx, strings.TrimSpace(active.ID), process.ScheduledTurns{StartHeartbeat: s.startHeartbeatTurn})`。`agents_api.go:158` 走 `bindSchedulerTo`，不用改。

### 5. 历史接口与网页

- `pkg/gateway/api_extra.go`：`msg` 结构新增 `Origin string \`json:"origin,omitempty"\``；组装时 `m.Origin = state.MessageOrigin(t.PartsJSON)`（放在附件那几行旁边）。
- `frontend/src/lib/api.ts`：`ChatMessageRecord` 新增 `/** Who wrote a user message on the person's behalf ('heartbeat'). */ origin?: string`。
- `frontend/src/composables/useChatStream.ts`：
  - `ChatMessage` 新增 `/** Who wrote a user message on the person's behalf: 'heartbeat'. */ origin?: string`。
  - `conversationFromTranscript` 的 user 分支加 `...(row.origin ? { origin: row.origin } : {})`。
  - 新增 `function applyHeartbeatFiredEvent(evt: ForebrainRunEvent, historical: boolean)`，规则和 `applyAutoContinueEvent` 的 `auto_continue_started` 分支一样：`historical || (sequence > 0 && sequence <= observerHighWater)` 时什么都不做；否则在 `prompt` 非空时追加 `{ id: \`heartbeat-${evt.id || Date.now()}\`, role: 'user', content: prompt, origin: 'heartbeat' }`。
  - 在 `:1712` 和 `:3052` 两处拦截自动继续事件的旁边，各加一个对 `'heartbeat_fired'` 的同样拦截（先 `rememberObservedEvent`，再 `applyHeartbeatFiredEvent`，然后 `return`）。
- `frontend/src/lib/forebrainGatewayRuntime.ts`：联合类型加 `| 'heartbeat_fired'`。
- `frontend/src/views/ChatView.vue:214-215`：保持 `v-else-if` 链不断，把用户分支改成一个外层 `div`，里面放两样：`msg.origin === 'heartbeat'` 时显示的标签 `<p data-testid="message-origin" class="mb-1 text-[11px] font-medium text-[var(--forebrain-muted-text)]">{{ t('chat.originHeartbeat') }}</p>`，以及原来那个 `whitespace-pre-wrap break-words` 的 `div`（内容不变）。
- `frontend/src/locales/index.ts`：中文 `'chat.originHeartbeat': '由心跳发送'`，英文 `'chat.originHeartbeat': 'Sent by the heartbeat'`。
- `frontend/src/lib/providerError.ts`：`formatProviderError` 认识新代码 `session_awaiting_approval`，映射到新键 `runError.sessionAwaitingApproval`（中文 `这个对话正在等待审批，请先处理审批再发送。`，英文 `This conversation is waiting for an approval; answer it before sending another message.`）。在函数注释里写明：它格式化的是所有带代码的回合失败，不只是提供方的错误。`RunErrorBlock` 已经在绘制时调用它，无需改动。

### 6. 终端重放（决策 D1）

- `pkg/tui/reducer.go:1432-1434` 默认分支：标题改为 `firstNonEmpty(msg.Title, "system")`，和 `MsgKindError` 分支的写法一致。
- `pkg/tui/commands.go` 的 `replayTurnWithReducer`：在 `if content == "" { return }` 之后、构造普通 `msg` 之前加：

  ```go
  	// A message the person did not type — a heartbeat's prompt — replays as
  	// what it is rather than as a card that says they sent it.
  	if kind == MsgKindUser {
  		if origin := state.MessageOrigin(turn.PartsJSON); origin != "" {
  			renderReplayFrames(renderer, reducer, Message{Kind: MsgKindSystem, Title: origin, Content: content, Timestamp: timestamp}, "", meta, timing)
  			return
  		}
  	}
  ```

## 缓存影响（必须核对）

- **对话之前的前缀**：不变。没有改工具表、system，也没有改注入在对话前的开发者指令。
- **心跳回合本身**：从 agent 级基础 runner 改到该会话自己的 runner（执行器按会话解析 runner，项目会话拿到项目 runner），查询来源从 `agent:default` 变成和网页回合相同的 `repl_main_thread`。于是心跳请求的前缀（工具 + system + 每会话只渲染一次的记忆指令）和这个会话其它回合**逐字节相同**，能命中这个会话已建好的缓存。今天项目会话的心跳用的是另一套工具表，每次都是冷前缀。
- **之后的回合**：心跳的提示和回答追加在消息尾部，下一回合的前缀就是"上一回合前缀 + 心跳这一段"，而心跳请求本身已把这一段写进了缓存。
- **来源标记**：只用于展示，`ParseMessageParts` 忽略它，发给模型的内容与手打同样文字完全一致（第 1 步的测试钉住这一点）。
- **测量**：第 8 步用 `fb_runs` 的用量列算同一会话里心跳回合的命中率，`cache_read / (cache_read + cache_creation + prompt)`，改动前后各测一次，结果写进本计划末尾的"执行记录"。

## 需要的命令

见 README"常用命令"。本计划额外用到：

| 用途 | 命令 | 成功标志 |
| --- | --- | --- |
| 本计划相关 Go 测试 | `CGO_ENABLED=1 go test -tags fts5 ./pkg/state/ ./pkg/turn/ ./pkg/event/ ./pkg/process/ ./pkg/gateway/ ./pkg/tui/ -count=1` | 全部 `ok` |
| 前端相关单测 | `cd frontend && corepack pnpm exec vitest run src/composables/useChatStream.test.ts` | 全部通过 |

## 范围

**要改的文件（只改这些）**：

- `pkg/state/message_parts.go`、`pkg/state/message_parts_test.go`
- `pkg/turn/session.go`、`pkg/turn/session_test.go`
- `pkg/turn/submit.go`、`pkg/turn/submit_test.go`
- `pkg/turn/scheduler.go`、`pkg/turn/scheduler_test.go`
- `pkg/event/run_events.go`
- `pkg/process/cron_service.go`，以及新建的 `pkg/process/cron_service_test.go`（测试文件允许新建，名字必须对应 `cron_service.go`）
- `pkg/gateway/run_control.go`、`pkg/gateway/run_control_test.go`、`pkg/gateway/server.go`、`pkg/gateway/api_extra.go`、`pkg/gateway/api_extra_test.go`
- `pkg/tui/commands.go`、`pkg/tui/reducer.go`、`pkg/tui/commands_test.go`
- `frontend/src/lib/api.ts`、`frontend/src/lib/forebrainGatewayRuntime.ts`、`frontend/src/lib/providerError.ts` 及其测试、`frontend/src/composables/useChatStream.ts`、`frontend/src/composables/useChatStream.test.ts`、`frontend/src/views/ChatView.vue`、`frontend/src/locales/index.ts`
- 新建 `frontend/e2e/heartbeat.spec.ts`
- `docs/plan/SCHEDULED_RUNS_TRANSCRIPT/README.md`（状态行）

**不要碰**：

- `pkg/turn/scheduler.go` 里定时任务的 `fire`/`runDueJobs`/`RunNow`，以及 `RunPrompt` 的定时任务用法，那是计划 005 的事。`pkg/process/one_shot.go`、`worker_cli.go` 本计划不删：定时任务还在用它们。
- 自动继续的调度逻辑（`pkg/turn/submit.go` 的 `autoContinuer`）和它的事件形状。
- 网页自己的回合入口（`pkg/gateway/server.go` 的 WS 发送路径）。
- 任何注入在对话前的提示内容（`pkg/run/memory_llm.go`、`pkg/assembly`、`pkg/agent`）。
- `pkg/gateway/dist`。

## 步骤

### 第 0 步：记录基线

运行 README 常用命令里的 Go 全量测试、`deadcode`、前端单测，把 `deadcode` 的输出保存到 scratch 目录作基线。

**验证**：Go 全量测试全部 `ok`；`deadcode` 只有 `NewRuntimeWithStore` 一行；前端单测全部通过。基线若有失败，按 STOP 条件处理。

### 第 1 步：消息来源标记

按设计 §1 改 `pkg/state/message_parts.go`。测试写进 `pkg/state/message_parts_test.go`：

- `WithMessageOrigin` 之后 `MessageOrigin` 读回 `"heartbeat"`；普通 parts 读回 `""`。
- **缓存不变式**：对 `p := state.MessagePartsJSON(llm.UserMessage(llm.Text("anything new?")), "anything new?")`，`ParseMessageParts(WithMessageOrigin(p, "heartbeat"), "")` 的四个返回值和 `ParseMessageParts(p, "")` 完全相等（用 `reflect.DeepEqual`）。

**验证**：`CGO_ENABLED=1 go test -tags fts5 ./pkg/state/ -run 'Origin' -count=1 -v` → 新测试 PASS。

### 第 2 步：引擎层（UserTurn.Origin、ParkedOnApproval、事件）

按设计 §2 改 `pkg/turn/session.go`、`pkg/turn/submit.go`、`pkg/event/run_events.go`。测试：

- `pkg/turn/session_test.go`：`PersistUserTurn` 带 `Origin: state.MessageOriginHeartbeat` 时，存下的行 `state.MessageOrigin(row.PartsJSON) == "heartbeat"`，`content` 等于提示原文；不带时为 `""`。
- `ParkedOnApproval`：没装闸门返回 false；闸门的 `Pending` 返回非 nil 时返回 true。用一个实现了 `turn.ApprovalGate` 的测试替身，通过 `turn.WithApprovalGate` 注入。

**验证**：`CGO_ENABLED=1 go test -tags fts5 ./pkg/turn/ ./pkg/event/ -count=1` → `ok`。

### 第 3 步：调度器只负责"何时"，"能否开始"交给启动方

按设计 §2 改 `pkg/turn/scheduler.go`，按 §4 改 `pkg/process/cron_service.go` 和 `pkg/gateway/server.go`（`startHeartbeatTurn` 先写一个只返回错误的占位，第 4 步再实现；这一步结束时必须能编译）。

改 `pkg/turn/scheduler_test.go` 里所有用到 `SessionBusy` 的心跳测试（`:200-251`、`:395-428`、`:430-458`），换成 `StartHeartbeat` 替身：

- 忙：替身返回 `fmt.Errorf("x: %w", ErrSessionBusy)` → 不记 `last_fired_at`，`next_run_at == now + 间隔`。
- 空闲：替身记录 `(sessionID, prompt)` 并返回 nil → 记 `last_fired_at == now`，`next_run_at == now + 间隔`，替身收到 `("s-1", "anything new?")`。
- 其它错误：不记 `last_fired_at`。
- "执行中被清除不复活"：替身里删掉心跳并返回 nil → 之后 `GetHeartbeat` 为 nil。
- 别的租户的心跳不触发：保持原断言。

`pkg/process/cron_service_test.go`（新建）：`Bind` 后 `CronService` 把 `ScheduledTurns.StartHeartbeat` 交给了调度器。做法：环境里存一个已到期的心跳，调 `c.sched.RunDue(ctx)`，断言替身被调用。环境构造参照 `pkg/process/worker_cli_test.go` 里的 `envWithMCPServers`。

**验证**：`CGO_ENABLED=1 go test -tags fts5 ./pkg/turn/ ./pkg/process/ -count=1` → `ok`；`grep -rn "SessionBusy" pkg --include='*.go'` → 无输出。

### 第 4 步：gateway 无人值守回合一般化与心跳启动器

按设计 §3 改 `pkg/gateway/run_control.go`，实现 `startHeartbeatTurn`。

测试写进 `pkg/gateway/run_control_test.go`，复用 `newAutoContinueGateway`：

1. `TestHeartbeatRunsAsATurnOfItsConversation`：调 `g.server.startHeartbeatTurn(ctx, "session-ws", "anything new?")`，从已绑定的 WS 依次读到 `heartbeat_fired`（payload `prompt` 为原文）→ `turn_started` → `turn_completed`，三者 `run_id` 相同；`g.executor.last()` 的 `Trigger == "heartbeat"`、`Origin.ChannelID == "heartbeat"`、`Origin.Surface == turn.SurfaceWebChat`；`ListRecentMessages` 得到 `user, assistant` 两行，user 行 `state.MessageOrigin(...) == "heartbeat"`、`RunID` 等于该运行；`worked` 时钟已盖（`SessionStore` 读到该运行的 `RunWorkedMs > 0`，或用 `turn.RunWorkedLines(rows)` 能找到该运行）。
2. `TestHeartbeatStandsDownWhenTheSessionIsBusy`：`runController().Track("run-in-flight", "session-ws", ...)` 后调用，返回值 `errors.Is(err, turn.ErrSessionBusy)`，执行器没有收到请求，会话里没有新行。
3. `TestHeartbeatStandsDownWhenTheSessionIsParked`：夹具的 `Core` 换成带 `turn.WithApprovalGate(替身)` 的版本，替身报告有挂起审批 → 同上，返回 `ErrSessionBusy`，无新运行、无新行。
4. `TestDetachedTurnRefusedByAnotherRunsApprovalEndsFailed`：替身闸门在第一次 `Pending` 调用（`ParkedOnApproval` 的预检）时返回 nil，从第二次起（`Submit` 内部）都返回非 nil → 运行以 `turn_error` 结束，消息为设计里那句话；该运行状态为 failed；运行时钟已盖。
5. 自动继续的既有测试（`TestAutoContinuationRunsAsADetachedWebTurn`、`TestAutoContinuationStandsDownWhenTheSessionIsBusy` 等）**原样**通过。
6. `TestDetachedRunParkedOnApprovalBindsItsSnapshotRows`（基线没有直接覆盖 `parkDetachedRunOnApproval` 的测试，新写一个）：让夹具的执行器返回 `TurnWaitingApproval` 加 `Resume`（含一段 `SessionSnapshot`），或直接返回带 `SessionSnapshot` 的 `tool.RequiresActionError`。断言快照写出的每一行 `RunID` 都等于该运行。

**验证**：`CGO_ENABLED=1 go test -tags fts5 ./pkg/gateway/ -count=1` → `ok`。

### 第 5 步：历史接口带出来源

按设计 §5 改 `pkg/gateway/api_extra.go`。在 `pkg/gateway/api_extra_test.go` 里，用已有的 `handleChatMessages` 测试方式（`grep -n "handleChatMessages" pkg/gateway/api_extra_test.go`）补一条：带来源标记的 user 行返回 `"origin":"heartbeat"`，普通行没有 `origin` 键。

**验证**：`CGO_ENABLED=1 go test -tags fts5 ./pkg/gateway/ -run 'ChatMessages' -count=1` → `ok`。

### 第 6 步：网页

按设计 §5 改前端。测试写进 `frontend/src/composables/useChatStream.test.ts`，仿照 `describe('auto-continue after a usage limit')`：

- 绑定后 `high_water: 5`；序号 4 的 `heartbeat_fired` 不追加消息；序号 6 的 `heartbeat_fired` 追加一条 `{role:'user', content: prompt, origin:'heartbeat'}`；同一事件再来一次不重复追加。
- `conversationFromTranscript([{ role: 'user', content: 'anything new?', origin: 'heartbeat' }, ...])` 产出的 user 消息带 `origin: 'heartbeat'`。

**验证**：`cd frontend && corepack pnpm test` 全部通过；`cd frontend && corepack pnpm exec vue-tsc --noEmit -p tsconfig.json` 退出码 0。

### 第 7 步：终端重放

按设计 §6 改 TUI。先检查默认分支改标题的影响面：`grep -n "Kind: *MsgKindSystem" pkg/tui/*.go | grep -v _test`。对每一处，确认它**没有**设置 `Title`（基线只有 `chat_surface.go:106` 一处，不带 `Title`）。有设置 `Title` 的就按 STOP 条件处理。

测试写进 `pkg/tui/commands_test.go`，参照该文件里已有的重放测试（`grep -n "func Test.*Replay" pkg/tui/commands_test.go`）：一条带心跳来源的 user 行，重放出标题为 `heartbeat`、内容为提示原文的 `FrameSystem`，并且**没有** `FrameUser`；普通 user 行仍重放为 `FrameUser`。

**验证**：`CGO_ENABLED=1 go test -tags fts5 ./pkg/tui/ -count=1` → `ok`。

### 第 8 步：真机验证（网页 + 终端 + 缓存）

1. **网页 e2e（假模型）**：新建 `frontend/e2e/heartbeat.spec.ts`，参照 `frontend/e2e/cron-builder.spec.ts` 的结构和 `support.ts` 的 `signIn`、`expectAssistantReply`。流程：
   - `page.request.post('/api/chat/sessions', { data: {} })` 建会话，取 `id`。
   - `page.goto('/?session=' + id)`，发一条消息，`expectAssistantReply`。
   - `page.request.put('/api/heartbeat', { data: { session_id: id, interval_seconds: 60, prompt: 'e2e heartbeat ping' } })`。
   - 在 `150_000` ms 内等到 `[data-testid="message-origin"]` 可见，且它所在的消息含 `e2e heartbeat ping`；之后出现一条新的助手回复和 "Worked for"/"已工作" 行。
   - `page.reload()` 后以上内容仍在。
   - 最后 `DELETE /api/heartbeat?session_id=<id>` 清理。

   运行 `scripts/acceptance/web_e2e.sh` → 最后一行 `web e2e: PASS`（调度器 30 秒一跳加 1 分钟最小间隔，这个用例约需 90 秒）。
2. **网页 e2e（智谱真模型）**：`FOREBRAIN_E2E_REAL_LLM=1 scripts/acceptance/web_e2e.sh` → `web e2e: PASS`。
3. **终端重放（智谱真模型）**：用一个隔离的 `FOREBRAIN_HOME`，配置照抄 `scripts/acceptance/web_e2e.sh:78-98` 的真模型块（密钥只用 `${FOREBRAIN_E2E_ZHIPU_KEY}` 引用）。启动 `forebrain gateway start`，按上面的方法建会话、发一条消息、设 60 秒心跳，等到 `fb_messages` 里出现带来源标记的 user 行和它后面的 assistant 行（`sqlite3 "$FOREBRAIN_HOME/…/state.db" "select role, substr(content,1,40), run_id from fb_messages where session_id='<id>' order by id"`，库文件路径用 `find "$FOREBRAIN_HOME" -name '*.db'` 找）。然后停掉 gateway，在 tmux 里用同一个 `FOREBRAIN_HOME` 运行 `forebrain resume <id>`，用 `tmux capture-pane -p` 截屏：应看到 `● heartbeat` 卡片，下面是提示原文，再下面是模型回答和 "Worked for …" 行。
4. **缓存命中率**：对第 3 步的会话执行

   ```sql
   select run_id_kind, sum(usage_cache_read_tokens)*1.0/(sum(usage_cache_read_tokens)+sum(usage_cache_creation_tokens)+sum(usage_prompt_tokens))
   from (select case when id in (select run_id from fb_messages where session_id='<id>' and parts_json like '%"origin"%') then 'heartbeat' else 'user' end as run_id_kind, *
         from fb_runs where session_id='<id>') group by run_id_kind;
   ```

   改动前的基线用独立工作树得到：`git worktree add "$SCRATCH/baseline" 1a6d708`，在那里 `CGO_ENABLED=1 go build -tags fts5 -o "$SCRATCH/forebrain-baseline" ./cmd/forebrain`，用这个二进制跑同样流程（**不要** `git stash` 或切分支：工作区里有别人的未提交改动），测完 `git worktree remove "$SCRATCH/baseline"`（基线没有来源标记，就按 `fb_runs.input_text` 等于心跳提示来区分心跳运行）。按 README 全局规则 11：DeepSeek/OpenAI 有凭据就测；没有就记"凭据缺失，已跳过"，并记录智谱的数字。**心跳运行的命中率不得低于基线。**

**验证**：两次 e2e 都 PASS；tmux 截屏里有 `heartbeat` 卡片、回答和 Worked for 行；命中率数字写进下面的"执行记录"。

## 测试计划（汇总）

| 层 | 文件 | 用例 |
| --- | --- | --- |
| state | `pkg/state/message_parts_test.go` | 来源写入与读回；解析结果与无标记时完全相等（缓存不变式） |
| turn | `pkg/turn/session_test.go`、`pkg/turn/submit_test.go`、`pkg/turn/scheduler_test.go` | 来源落库；`ParkedOnApproval`；心跳的忙/空闲/失败/清除/租户隔离 |
| process | `pkg/process/cron_service_test.go`（新建） | `Bind` 把 `StartHeartbeat` 交给调度器 |
| gateway | `pkg/gateway/run_control_test.go`、`api_extra_test.go` | 心跳回合完整（事件、请求、行、时钟）；忙；停在审批；`Submit` 拒绝；自动继续回归；停审批快照行带 run_id；历史接口带 origin |
| tui | `pkg/tui/commands_test.go` | 心跳行重放为 `heartbeat` 系统卡片 |
| web | `frontend/src/composables/useChatStream.test.ts` | 实时追加规则；历史投影带 origin |
| e2e | `frontend/e2e/heartbeat.spec.ts` | 假模型与真模型各一遍 |

## 完成标准（全部满足）

- [ ] README 常用命令表里的每一条都达到成功标志
- [ ] `grep -rn "SessionBusy\|runAutoContinuation\|heartbeatChannelID" pkg --include='*.go'` 无输出
- [ ] `grep -n "AppendMessageSequence(ctx, sid, gate" pkg/gateway/run_control.go` 无输出
- [ ] `deadcode` 输出与第 0 步基线一致
- [ ] e2e（假模型、智谱）都 `web e2e: PASS`，`heartbeat.spec.ts` 在其中
- [ ] tmux 截屏证据和缓存命中率数字已写入下方"执行记录"
- [ ] `git status` 里只有"范围"列出的文件有改动
- [ ] README 状态行已更新

## STOP 条件

出现以下情况立即停止并报告：

- "现状"里的任何摘录和实际代码对不上。
- `pkg/turn/submit.go` 的 `Submit` 在挂起审批时返回的形状不是 `TurnWaitingApproval` 且 `Resume == nil`（第 4 步的测试 4 依赖这一点）。
- 改 `reducer.go` 默认分支标题时，发现有现存的 `MsgKindSystem` 消息设置了 `Title`。
- 任何一步需要新建生产 `.go` 文件，或者需要给 gateway/tui/run 新增仓库内部包的 import。
- 心跳运行的缓存命中率低于基线。
- 自动继续的既有测试需要改断言才能通过（说明一般化改变了它的行为）。
- 某一步的验证在一次合理修正后仍失败。

## 维护说明

- `startDetachedTurn` / `runDetachedTurn` 现在是所有"运行时替用户发起的回合"的唯一入口（自动继续、心跳，计划 005 加上定时任务）。以后再加这类回合，往 `detachedTurn` 里填字段，不要再复制一份 `runAutoContinuation`。
- "会话能否开始一个新回合"在本计划里由启动方用 `Find` 加 `ParkedOnApproval` 判断，只覆盖本进程。计划 004（决策 D8）会把它换成数据库里的原子规则（跨进程成立），并删掉 `ParkedOnApproval`、`turn.ErrSessionBusy` 和 `runDetachedTurn` 里"`Submit` 拒绝"的分支；本计划先按单进程把心跳做对，不要提前做 004 的事。
- `PartTypeOrigin` 只用于展示。任何读 parts 的新代码都不得把它拼进发给模型的内容；第 1 步的不变式测试守着这一点。
- 心跳间隔现在从触发**开始**起算（D2，owner 已选 A）。

## 执行记录

- 执行于 2026-10-05，HEAD `bda9505`（README"与并行工作"的 LSP 改动已由 owner 提交，工作区无干扰）。Go 车道 19 文件 + 前端车道 9 文件全部按设计落地；`./pkg/state/ ./pkg/turn/ ./pkg/event/ ./pkg/process/ ./pkg/gateway/ ./pkg/tui/` 全 `ok`；全量 Go 29 包 `ok`；前端 250 用例过、`vue-tsc` 0；`deadcode` 与基线逐行一致；`gofmt`/`go vet` 干净。自动继续既有测试未改一行、原样通过。
- 完成标准 grep：`runAutoContinuation`、`heartbeatChannelID` 零命中；`SessionBusy` 仅剩设计 §2 要求新增的 `turn.ErrSessionBusy` 自身（计划 004 按维护说明删除）——计划完成标准的 grep 子句与设计 §2 字面矛盾，按设计意图判定通过。`AppendMessageSequence(ctx, sid, gate` 无输出。
- e2e（假模型）：第一轮 74 过 2 败，两个失败为**既有缺陷**（LSP 提交 `bda9505` 给设置页加了第 10 个标签 `lsp`、项目空间加了第 9 个标签，`frontend/e2e/tenant-shell.spec.ts` 与 `project-space.spec.ts` 的数量断言未同步）。按"发现既有 bug 必须根因修掉"已修正断言（10 个 + `Language servers`/`语言服务器`；9 个）。第二轮 **76 passed，`web e2e: PASS`**（含本计划 `heartbeat.spec.ts`，假模型 1.2m）。
- e2e（智谱真模型 `FOREBRAIN_E2E_REAL_LLM=1`）：**77 passed，`web e2e: PASS`**。
- 终端重放（智谱，隔离 FOREBRAIN_HOME）：心跳 60s 连续多跳后停 gateway，`forebrain resume <id>`（tmux 截屏）：每跳呈现为 `● heartbeat` 卡片 + 提示原文 `heartbeat cache probe: 一句话汇报当前时间` + `Ran date …` 工具卡 + 回答 + `─ Worked for 6s/8s ─` 收尾行；`fb_messages` 中 user 行 `parts` 带 `{"kind":"heartbeat","type":"origin"}`，`run_id` 归属该跳运行。
- 缓存命中率（同一提示、智谱 `glm-5.3-flash`、各 4 次心跳运行）：基线（`1a6d708` 独立工作树二进制）**62.09%** → 改后 **86.87%**，不低于基线 ✓（心跳请求现在命中会话已建缓存；基线侧 `fb_messages` 为空也印证了改动前的旁路行为）。DeepSeek/OpenAI：凭据缺失，已跳过。
