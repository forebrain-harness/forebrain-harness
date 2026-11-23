# 主视图 Esc「打断并立即发送」下沉到底层引擎（对齐子代理视图）

> 状态：已执行（2026-10-29，主树 `dev` in-tree、零 commit，真机验收记录见文末 `## Implementation Status`）
> 日期：2026-10-29
> 基线：`dev` / `bc3339b7`，工作区有并行会话的 43 个未提交改动（见 §0.2）
> 落地位置：批准后复制为 `plans/193-main-view-esc-interrupt-to-send.md` 并在 `plans/README.md` 追加索引行
> 范围：`pkg/run`（引擎）、`pkg/tui`、`pkg/gateway`、`frontend/src`

## Owner 裁决（2026-10-29，执行期不得再改）

1. **队列为空 + 工具调用进行中**：Esc 与现状一致（取消 run）。原文：「如果 Message queue 队列为空，
   工具调用还在进行中，用户按下 esc 的效果与现状保持一致」。推广到"队列为空"的所有子情形
   （模型调用在飞 / 工具在跑 / 停在审批）：一律取消，由 `InterruptToSend` 返回 false 实现。
2. **队列有 steer + run 停在工具审批**：也走 interrupt-to-send，不取消审批、不结束 run；
   steer 留在队列，审批决定后恢复的那次调用立即带上它。
3. **Ctrl+C / SIGINT + 队列有 steer**：保持取消（`BoundaryInterruptToSend` 重发路径保留，不是死代码）。

---

## 0. 基线、drift check、并行 WIP

### 0.1 基线

- 分支 `dev`，HEAD `bc3339b7`。
- 本计划所有 file:line 与代码摘录都取自**当前工作区**（含并行会话未提交改动），不是 HEAD。

### 0.2 并行会话的未提交改动（只归因，不许回滚）

`git status --porcelain` 共 43 条。与本计划**同文件**的已脏路径：

| 路径 | 并行改动规模 | 并行改动位置 |
|---|---|---|
| `pkg/tui/run.go` | +46 行 | ~949、~1179、~1300-1400、~2465-2540、~4822-5003（**不含** ESC 分支 4638-4726） |
| `pkg/tui/run_test.go` | +75 行 | 分散 |
| `pkg/tui/render.go` | 241 行 | 渲染层 |
| `pkg/tui/chat_session_test.go`、`pkg/tui/chat_surface_test.go` | 80/99 行 | 测试 |

执行前必须做的归因步骤：

```bash
git -C <repo> diff --stat -- pkg/tui/run.go pkg/tui/run_test.go
git -C <repo> diff -- pkg/tui/run.go | grep -n '^@@'
```

只许**追加**本计划的 hunk；上面列出的并行 hunk 原样保留。若 `git diff` 显示并行改动已经覆盖到 ESC 分支（4638-4726）或
`TestEscInTheMainViewIsUnchanged`（run_test.go:5924-5955），**STOP** 并报告，不要合并两边的改写。

**明确不碰**（别的会话正在改，diff 归因会失真）：`pkg/run/config.go`、`pkg/run/runner.go`、
`pkg/tool/*`、`pkg/llm/stream_retry*.go`、`pkg/llm/stream_test.go`、`pkg/turn/approval.go`、
`pkg/agent/subagent_defs.go`、`pkg/agent/advisor_prompts*.go`、`scripts/acceptance/fake_provider.py`、
`scripts/acceptance/native_advisor_workflows.sh`、`plans/186..192`、`plans/README.md` 的其它行。

### 0.3 drift check（内容不符即 STOP）

| 锚点 | 命令 | 期望 |
|---|---|---|
| ESC 分支 | `grep -c 'esc to interrupt and send immediately' pkg/tui/run.go` | `2`（4052、4706） |
| 取消帧 | `grep -c 'Title: "cancelling run"' pkg/tui/run.go` | `8`（2065、3827、3839、4717、4724、4736、4748、5103；改后仍是 8 —— 4717 留在 fallback 里） |
| 子代理引擎 | `grep -n 'func (ch \*subagentChannel) interruptToSend' pkg/run/subagent.go` | `801` |
| 每调用取消 | `grep -n 'The call runs under its own cancellation' pkg/run/orchestration_llm.go` | `460` |
| 队列边界 | `grep -n 'BoundaryInterruptToSend' pkg/run/turn_input.go` | `805`（注释）与 `840`（分支） |
| 网关边界注释 | `grep -n 'that boundary belongs to surfaces' pkg/gateway/run_control.go` | `202` |
| web 入口 | `grep -n 'function sendPendingSteersAfterInterrupt' frontend/src/composables/useChatStream.ts` | `6135` |

行号漂移不算 drift（并行会话会挪行）；**内容**（整段字符串）不符才算。

---

## 1. 目标（Why this matters）

owner 截图（2026-10-29）：主视图里 composer 上方已有排队消息，用户按 Esc，得到三件事：

1. 主视图多出一行状态 `cancelling run`；
2. 当前 run 结束，正文里多出一条 `— Worked for 2m 09s · first token 10.7s · 11:32 —`；
3. 「Working (… • esc to interrupt)」的计时被重置（新 run 从 0 开始），排队消息作为**新的一轮**发出。

同样的操作在**子代理视图**里是另一套效果（owner 认为这才是正确的）：排队消息出队并立刻发出，**没有** `cancelling run`，
**没有** `Worked for`，Working 计时继续累加，看上去当前 run 并没有被取消。

要求：把子代理视图这套语义**下沉到引擎**，主视图与主代理会话对齐，并且 TUI 与 web 共用同一份实现，
不许在两个 surface 各自打补丁。

## 2. 现象复现（修复前必须先看到红）

### 2.1 现状的行为是有测试钉住的（这就是"红"的现场）

`pkg/tui/run_test.go:5922-5955` `TestEscInTheMainViewIsUnchanged`：

```go
	// Interrupt to send pending steers.
	steer := &fakeSession{cancelActive: true}
	steerState := &streamState{sessionID: "s1", session: steer}
	steer.enqueueSteer("s1", mustComposerSubmission("keep going"))
	handleActiveRunInput(context.Background(), steer, renderer, nil, steerState, nil,
		inputEvent{kind: inputEventHotkey, hotkey: hotkeyEscapeInterrupt}, nil)
	if !steerState.submitPendingSteersAfterInterrupt || !steerState.userInterrupted {
		t.Fatal("main-view Esc with pending steers must interrupt and send")
	}
```

它断言的正是 owner 不要的行为：主视图 Esc 有排队 steer 时走「标记 + `CancelActiveRun`」。
本计划把这条用例的**契约改写**为新语义（见 Step 6），并新增引擎侧用例（Step 3）。

### 2.2 复现步骤（真机，Step 8 会做完整版）

假 provider 起一个会长时间流式输出的主 run → 在 composer 里再打一条消息回车（steer 入队）→ 按 Esc：
修复前 `cancelling run` + `Worked for …` 出现、Working 计时归零；修复后两者都不出现，steer 直接以用户消息出现在正文里，
Working 计时连续。

## 3. 根因（证据链）

### 3.1 子代理视图：引擎有「打断即发送」这个原语

- `pkg/tui/run.go:2591-2613` `handleSubagentViewEsc`：第一件事就是 `session.InterruptSubagentToSend(state.sessionID, view)`，
  成功即 `return` —— **不渲染任何帧**，所以没有 `cancelling run`。
- `pkg/tui/chat_session.go:1608-1611` → `pkg/run/subagent.go:2244-2254` `InterruptSubagentToSend` →
  `pkg/run/subagent.go:797-815` `subagentChannel.interruptToSend`：

  ```go
  	ex := ch.running
  	if ex == nil || len(ch.queue.Preview().Steers) == 0 {
  		ch.mu.Unlock()
  		return false
  	}
  	ex.boundary = BoundaryInterruptToSend
  	...
  	cancel(errSubagentInterruptedToSend)
  ```

  被取消的是 `subagentExecution` 的**一次 attempt**，不是 channel、不是 execution。
- `pkg/run/subagent.go:918-975` `runSubagentExecution` 是 attempt 循环：

  ```go
  		if boundary == BoundaryInterruptToSend && ch.store != nil {
  			if steers, _ := ch.queue.Next(BoundaryInterruptToSend); len(steers) > 0 {
  				ch.publishInputDelivered(fac, ex, steerEntries(steers))
  				in, skills := mergeSteers(steers)
  				attempt = subagentResumeAttempt(fac, record, in, skills)
  				continue
  			}
  		}
  ```

  下一 attempt 仍在**同一个 execution** 里 → `reportSubagentExecutionEnded` 不会触发 → 子代理视图拿不到 end 事件，
  所以既没有 `Worked for`，Working 计时（`SubagentExecutionStarting` 起的那个钟）也不重置。
- 钉住这条语义的用例：`pkg/run/subagent_test.go:5345-5411` `TestInterruptToSendContinuesInTheSameExecution`
  （断言 `subagent_spawned`/`subagent_ended` 各 1 次、executor 跑 2 次 → 一个 execution、两次 attempt）。

### 3.2 主视图：主 run **没有**这个原语，只有"整个 run 取消"

- `pkg/tui/run.go:4638-4726` 主视图 Esc（`hotkeyEscapeInterrupt`）：`withdraw` → 跳过可选 MCP 启动 →
  有队列 steer 时 `state.markSubmitPendingSteersAfterInterrupt()` + `session.CancelActiveRun()` +
  `renderer.RenderFrame(Frame{Kind: FrameStatus, Title: "cancelling run"})`（4710-4718）。
- 取消到达引擎后整个 turn 结束：`pkg/tui/run.go:331-388` 的 `case RunEndedMsg:` 用
  `ev.WorkedStatus` 渲染 `FrameStatus`（383），而 `WorkedStatus` 在 `pkg/tui/reducer.go:1284-1361` 的
  `RunEndedMsg` 分支里算出、由 `formatWorkedStatusTitle`（同文件 **1836**）生成 —— 就是截图里的
  `Worked for 2m 09s · …`。
- Working 计时属于**这个 turn**：`pkg/tui/run.go:3727` `StartTimedTransientStatus(transientSourceWorking, …)`，
  文案在 `pkg/tui/run.go:4415`（`Working (elapsed • esc to interrupt)`）。turn 一结束，计时自然归零。
- 排队消息确实是"之后才发"的：`pkg/tui/run.go:4038-4087` `nextAutomaticSubmission` 在
  `runTurnInterrupted` 分支里消费 `run.BoundaryInterruptToSend`（4055-4061），把它当作**下一轮**的输入。
  也就是说 `BoundaryInterruptToSend` 这个语义在主路径上被应用在 **turn 边界**，代价就是 run 结束。
- 引擎侧边界注释已经把话说白（`pkg/gateway/run_control.go:198-209`）：

  ```go
  // ... The gateway has no path that interrupts a run in
  // order to send its steers; that boundary belongs to surfaces that know why
  // the interrupt was issued.
  ```

  主 run 的 `state.RunStatus` 只有 Done/…/Cancelled，`Cancelled` 一律映射到 `run.BoundaryInterrupted`。

### 3.3 web 与 TUI 是同构的两份

- 主视图：`frontend/src/components/ForebrainPromptTextarea.vue:653-657`（`duringRun` + Escape → `emit('interrupt-run')`）
  → `frontend/src/views/ChatView.vue:498/526` → `interruptAndSendPendingSteers`（1528-1530）→
  `frontend/src/composables/useChatStream.ts:6131-6137` `sendPendingSteersAfterInterrupt()` → `cancel()` →
  `POST /runs/:id/cancel`（`pkg/gateway/run_control.go:318-367` `handleRunCancel` → `cancelRun`（250-266）→
  驱动方结束时 `finishRun`（173-196），`Cancelled` 记 `BoundaryInterrupted`）。
- 子代理视图：`ChatView.vue:2402-2411` `handleSubagentEscape` → `interruptSubagentToSend(id)` →
  `POST /chat/sessions/:id/subagents/:agent/interrupt-send`（`pkg/gateway/api_extra.go:223`、`3083-3103`）。
- 所以 web 的主视图与 TUI 的主视图犯的是同一个错：Esc 只能"取消整个 run"。

### 3.4 引擎里已经存在"只放弃一次模型调用、不取消 run"的机制（这就是要归一到的既有语义）

`pkg/run/orchestration_llm.go:459-469`：

```go
		// The call runs under its own cancellation, so a retracted steer can
		// abandon it without cancelling the run.
		callCtx, abandonCall := context.WithCancelCause(ctx)
		if inFlightSteers != nil {
			...
			inFlightSteers.bindAbort(func() { abandonCall(errSteerRetracted) })
		}
```

撤回 steer 时（`pkg/run/turn_input.go:285-310` `RetractSteer` + `pkg/run/orchestration_llm.go:590-636`）
引擎就是"放弃这次调用 → 把 steer 退回队列 → 用剩下的 steer 重新发一次调用 → 同一个 turn 继续"。
**「打断并立即发送」和它的唯一区别**：不把任何条目退回去，且保留已经产出的半截回答。

缺的那一块只有两点：

1. `bindAbort` 只挂在 `SteerDelivery`（只有"这次调用带着 steer"时才有），**普通的调用没有放弃句柄**，
   所以模型正在长篇流式输出、steer 还没送达时，没有东西可打断；
2. 循环里没有"被 `interrupt-to-send` 打断"这个分支——只有 retraction 分支（590-636）和 run 取消分支（559-589），
   后者会把错误返回给调用方，turn 也就结束了。

**根因一句话**：主 run 缺少引擎级的 interrupt-to-send 原语；surface 只能用"取消整个 run + 下一轮重发"
去近似，于是 run 结束、`Worked for` 出现、计时归零。子代理因为引擎有 `interruptToSend` + attempt 循环，
同一个 execution 内继续，所以没有这些副作用。

---

## 4. 设计（引擎单一来源，两个 surface 只做转发）

### 4.1 引擎：一次模型调用的放弃句柄（`pkg/run/turn_input.go`）

`TurnInputRuntime`（现 33-40 行）增加一个字段与方法组：

```go
type TurnInputRuntime struct {
	mu       sync.Mutex
	pending  []TurnInputEntry
	inflight []*SteerDelivery
	onChange []func(delivered []TurnInputEntry)
	// callAbort abandons the model call in flight; nil between calls. The
	// orchestration loop binds it for EVERY request (not only the ones
	// carrying a steer), which is what lets Esc stop a call that is already
	// streaming an answer nobody asked to continue.
	callAbort func()
}
```

新增：

```go
// bindCallAbort registers how to abandon the model call about to be made.
func (r *TurnInputRuntime) bindCallAbort(abort func())

// endCall retires the call's abandon handle once it returned.
func (r *TurnInputRuntime) endCall()

// InterruptToSend is Esc's "interrupt and send immediately" for the
// conversation's own turn: when a steer is waiting — queued, or carried by a
// call that has not settled — the call in flight is abandoned so the loop asks
// again carrying it, and the turn keeps running. It reports false when nothing
// is waiting, so the surface can fall back to Esc's other meanings.
func (r *TurnInputRuntime) InterruptToSend() bool
```

`InterruptToSend` 的判定与顺序：

1. 锁内统计"等待中的 steer" = `pending` 里 `Mode == TurnInputModeSteer` 的条目 + 每个未结算 `inflight` delivery 里的 steer 条目
   （与 `HasSteers()` 的差异：`inflight` 也算，因为那条 steer 还没被模型回答）。
2. 为 0 → 返回 false，什么都不动。**这条是 owner 的硬要求**（2026-10-29）：「如果 Message queue 队列为空，
   工具调用还在进行中，用户按下 esc 的效果与现状保持一致」——即队列空时 Esc 一律走旧的取消路径，
   无论此刻是模型调用在飞、工具在跑、还是停在审批上。
3. 非 0 → 取出 `callAbort` 并在**锁外**调用它（避免在调用方 goroutine 里做锁内回调），返回 true。
   `callAbort == nil`（调用之间、工具执行中、审批等待中）同样返回 true：消息会在下一次模型调用随
   `BeginSteerDelivery` 发出，这正是"打断并立即发送"要的结果，surface 不应再退回取消。
   工具正在长时间执行时，未开始的调用会被既有 `steerWaiting()`（`pkg/run/orchestration_llm.go:1352-1361`）
   读成 not-run，正在执行的那一个**不被中断** —— 这是与"取消 run"的唯一差别，owner 已按此裁决（见 §5 矩阵）。

### 4.2 引擎：循环里的新分支（`pkg/run/orchestration_llm.go`）

1. 在 `errSteerRetracted`（现 850 行）旁新增：

   ```go
   // errInterruptToSend is the cause a model call is abandoned with when the
   // user pressed Esc with steers waiting: the call stops where it is and the
   // same turn asks again carrying them. It is not a cancellation — nothing the
   // turn produced is thrown away and no turn boundary is reached.
   var errInterruptToSend = errors.New("interrupt to send the queued steers")
   ```

2. 每次调用前绑定放弃句柄（插在现 461 行 `callCtx, abandonCall := context.WithCancelCause(ctx)` 之后）：

   ```go
   		if rt := TurnInputRuntimeFromContext(ctx); rt != nil {
   			rt.bindCallAbort(func() { abandonCall(errInterruptToSend) })
   		}
   ```

3. 调用返回时**先取原因再清句柄**（插在现 521-522 行）：

   ```go
   		res, err := w.inner.Execute(callCtx, session, tools)
   		interruptedToSend := errors.Is(context.Cause(callCtx), errInterruptToSend)
   		abandonCall(nil)
   		if rt := TurnInputRuntimeFromContext(ctx); rt != nil {
   			rt.endCall()
   		}
   ```

   注意 `abandonCall(nil)`：`context.CancelCauseFunc` 在 cause 为 nil 时把 cause 记成 `context.Canceled`，
   所以原因必须在它**之前**读。

4. 新分支放在 `incomplete` 处理（现 536-546）之后、`refused := …`（现 559）之前，
   条件是"被打断且这次调用没有正常完成"：

   ```go
   		if interruptedToSend && (err != nil || res == nil || res.Message == nil) {
   			// Esc stopped this call to send what was waiting behind it. What
   			// the model already said stays in the turn; the steers the
   			// abandoned call was carrying go back to the queue so they ride
   			// the next call together with everything queued since; and the
   			// loop asks again instead of returning — the run does not end, so
   			// the surface keeps one clock and shows no "Worked for".
   			accumulateUsage(&totalUsage, usageCopy(res))
   			// The answer text this call produced ends here, exactly as it
   			// ends at a provider's text-block boundary: the surface seals the
   			// live block before the steer opens a user block, so the words it
   			// already showed stay their own block instead of being replaced
   			// by the answer the next call produces.
   			if sink := llm.StreamSinkFrom(callCtx); sink != nil && sink.OnTextBlockDone != nil {
   				sink.OnTextBlockDone()
   			}
   			// Unwind the abandoned call's steers first: the tail this
   			// truncation drops is exactly those user messages, and the
   			// partial answer is appended after it.
   			if inFlightSteers != nil && inFlightSteers.Rollback() {
   				session = session[:sessionBeforeSteers]
   			}
   			inFlightSteers = nil
   			heldAnswer = nil
   			if exec != nil && exec.dispatched() {
   				// Calls the stream already started settle here: the ones that
   				// finished keep their results, the rest read as interrupted.
   				// The run's own ctx is alive, so this is a settlement, not a
   				// cancellation.
   				settled := exec.abort(ctx)
   				if prov := collector.result().Message; prov != nil {
   					session = append(session, *prov)
   					noteResponseCompleted()
   				}
   				for _, result := range settled {
   					session = append(session, toolResultMessage(result))
   				}
   			} else if prov := collector.result().Message; prov != nil {
   				session = append(session, *prov)
   				noteResponseCompleted()
   			}
   			partialCapture.Set(session)
   			transcript.flush(context.WithoutCancel(ctx), session, modelForTranscript(), nil)
   			if delivery := TurnInputRuntimeFromContext(ctx).BeginSteerDelivery(ctx); delivery != nil {
   				sessionBeforeSteers = len(session)
   				session = append(session, w.steerMessages(ctx, delivery.Entries())...)
   				inFlightSteers = delivery
   				partialCapture.Set(session)
   			}
   			continue
   		}
   ```

   要点（执行者不要改顺序）：
   - `transcript.flush` 的第四个参数传 **`nil`**：`latestUsage` 在本函数里首次声明于 685 行（成功路径），插入点看不见它；
     同位置的取消分支（578 行）传的也是 `nil`。
   - `OnTextBlockDone` 是 provider 在"一次响应里文本块结束"时触发的同一个回调（`pkg/llm/stream.go:52-55`，
     google/anthropic 的实现在 `pkg/llm/google/stream.go:192`、`pkg/llm/anthropic/agent_llm.go:368`）；
     TUI 侧它会被映射成 `AssistantDoneMsg`（`pkg/tui/chat_turn.go:490-493` → `pkg/tui/reducer.go:1278-1283`
     → `flushAssistantFinal`），把这个 live 块封口。**不封口就会出真 bug**：`viewModel.append`
     （`pkg/tui/tty_session.go:1108-1122`）在收到 Final 的 assistant 帧时会**回退搜索最近一个非 Final 的 assistant 块并整体替换**，
     于是下一轮回答的 Final 会把半截回答的文本覆盖掉。`pkg/llm/stream_progress.go:213-217` 只是转发，无副作用。
   - **必须先 Rollback 再 append 半截回答**，否则 `session[:sessionBeforeSteers]` 会把刚 append 的回答一起截掉。
   - `exec.abort` 传的是 `ctx`（run 的上下文，仍存活），与 559-589 取消分支传 `ctx` 的写法一致；
     差别只在"settle 完继续"而不是 `return nil, err`。
   - 半截回答进入 `session` 成为普通的 assistant 消息（这就是子代理视图里"半截回答仍在"的等价物），
     `heldAnswer` 必须清空（它只服务于 retraction 的"退回到答案"语义）。
   - steer 在 `BeginSteerDelivery` 之后照 839-844 行的既有写法 append，保持 FIFO。
   - 如果没有 steer 可拿了（用户在这期间撤回），`delivery == nil`，循环继续，下一轮以半截回答正常收尾。

5. `Execute` 的文档注释（函数上方）补一段：一句话说明"被 Esc 打断的调用不是取消，turn 继续"。

### 4.3 引擎：Controller 与包级入口（`pkg/run/controller.go`）

```go
// InterruptToSend is Esc's "interrupt and send immediately" for the
// conversation's own turn: the run stops its current model call and goes on
// carrying the steers queued behind it. It is the primary counterpart of
// InterruptSubagentToSend: same meaning, same one-run contract — the run does
// not end.
func (c *Controller) InterruptToSend(runID string) bool {
	if c == nil {
		return false
	}
	state, ok := c.Get(runID)
	if !ok {
		return false
	}
	state.mu.Lock()
	q := state.input
	phase := state.phase
	state.mu.Unlock()
	if q == nil || phase == Finished {
		return false
	}
	rt := q.Runtime()
	if rt == nil {
		return false
	}
	return rt.InterruptToSend()
}
```

**不加** `run.InterruptToSend(r *Runner, sessionID)` 这种包级 helper：它只能从 `Runner.Control` 反查，而 TUI 侧
run 的真源是 `s.tuiControlLocked()`（裸 `ChatSession` 没有 env 时那是另一个对象）。两个调用点各自按"持有这些 run
的控制器"查找，见 §4.4 / §4.5。

> **承重事实（三种查法，别用错）**：产品路径上 `ChatSession.tuiControl = env.Control`（`pkg/tui/notify.go:1630`）、
> `Runner.Control = env.Control`（`pkg/process/open.go:424`）、网关 `Server.runController() = Env.Control`
> （`pkg/gateway/server.go:300-313`）**是同一个控制器**。但 `tuiControlLocked()`（`pkg/tui/chat_session.go:1366-1371`）
> 在没有 env 时会**新建**一个（测试就是这种情形），所以：
> ① `ChatSession.InterruptToSend` 必须走 `s.tuiControlLocked()` + `s.tuiRunIDLocked()`（与 `CancelActiveRun` 1439-1469
> 完全一致，TUI 的 run 就注册在它上面）；② 网关走 `s.runController()`；③ 不要写"从 `s.runner()` 取 Control"的第三种。

`InputQueue.Runtime()` 已存在（`pkg/run/turn_input.go:596`）。`Controller.track`（183-213）与 `Begin`（133-170）
两条注册路径都会设好 `state.input` 并 `Attach` 一个 runtime，所以这里不需要第二条查找路径。

### 4.4 TUI（`pkg/tui`）

1. `pkg/tui/notify.go` 的 `Session` 接口里、`CancelActiveRun() bool`（现 1882 行）旁边加：

   ```go
   	// InterruptToSend stops the conversation's in-flight model call so the
   	// steers queued behind it are sent now, inside the same turn — Esc's
   	// "interrupt and send immediately". False means nothing was waiting, so
   	// the surface keeps its other meanings for Esc.
   	InterruptToSend(sessionID string) bool
   ```

2. `pkg/tui/chat_session.go`，放在 `InterruptSubagentToSend`（1608-1612）旁边，**用 TUI 自己的控制器**
   （与 `CancelActiveRun` 1439-1469 同一套查找）：

   ```go
   // InterruptToSend is Esc's "interrupt and send immediately" in the
   // conversation's own view: the engine stops the model call in flight and the
   // turn goes on carrying the steers, exactly as a subagent's does. It reads
   // the controller that holds this session's TUI runs — the same one
   // CancelActiveRun resolves through — which a session without an environment
   // owns separately from the runner's.
   func (s *ChatSession) InterruptToSend(sessionID string) bool {
   	if s == nil {
   		return false
   	}
   	s.tuiRunMu.Lock()
   	ctl := s.tuiControlLocked()
   	runID := s.tuiRunIDLocked(strings.TrimSpace(sessionID))
   	s.tuiRunMu.Unlock()
   	if runID == "" {
   		return false
   	}
   	return ctl.InterruptToSend(runID)
   }
   ```

3. `pkg/tui/run.go` 主视图 Esc（现 4689-4725）。**先把 4697-4698 两行上移**（`state.composer.Text = ""` +
   `state.holdComposer = false`，它们丢弃 run 期间通过 `holdComposer` 暂存的粘贴/附件文本，防止 run 结束后
   idle loop 把它当下一轮自动提交；4689-4696 的注释就是在说这件事），**然后在 `state.markUserInterrupt()`（4704）之前**
   插入打断：

   ```go
   			// Esc with a steer waiting is one operation the engine owns: the
   			// call in flight is abandoned and this same turn goes on carrying
   			// the steer — the meaning Esc already has in a subagent's view.
   			// The run does not end, so there is no "cancelling run" line and
   			// no "Worked for" closing it, and the working clock keeps counting.
   			if session.InterruptToSend(state.sessionID) {
   				return
   			}
   ```

   顺序（照此排列，不要调换）：`withdraw`（4681-4688）→ 跳过可选 MCP 启动（4673-4675）→ 清 `composer.Text`/`holdComposer`
   → **打断**（新）→ `markUserInterrupt()` → 旧 fallback。
   注意：**不能**先 `state.markUserInterrupt()` —— 早退路径下 `userInterrupted` 必须保持 false，
   否则 turn 结束时会被当成中断边界、把队列还到 composer。同理 `submitPendingSteersAfterInterrupt` 也不能被置位。
   旧代码（标记 / 取消 / "cancelling run"）整段保留，作为"引擎说没有可打断的东西"时的 fallback
   （run 不在本进程、只有 shell 命令在跑、或队列里没有 steer）。

### 4.5 web（`pkg/gateway` + `frontend`）

1. `pkg/gateway/api_extra.go:180` 组里加一条路由，位置在 `/:id/file-changes/diff`（现 214 行）之后、
   subagent 注释块（现 215 行）之前：

   ```go
   	chatSessions.Post("/:id/interrupt-send", s.handleSessionInterruptSend)
   ```

2. `pkg/gateway/run_control.go` 新增 handler（与 `handleRunCancel` 相邻，复用 `sessionOwned`）：

   ```go
   // handleSessionInterruptSend is Esc's "interrupt and send immediately" for
   // the conversation itself: the run's model call in flight is abandoned and
   // the turn goes on carrying the steers queued behind it — no run end, no
   // "Worked for", the same clock. False leaves the surface its other meaning
   // for Esc, which is stopping the run.
   func (s *Server) handleSessionInterruptSend(w http.ResponseWriter, r *http.Request) {
   	if r.Method != http.MethodPost {
   		http.Error(w, "method", http.StatusMethodNotAllowed)
   		return
   	}
   	sid := strings.TrimSpace(ParamsFromContext(r.Context()).ByName("id"))
   	if sid == "" {
   		http.NotFound(w, r)
   		return
   	}
   	owned, err := s.sessionOwned(r.Context(), sid)
   	if err != nil {
   		http.Error(w, err.Error(), http.StatusInternalServerError)
   		return
   	}
   	if !owned {
   		http.NotFound(w, r)
   		return
   	}
   	runID, _, ok := s.runController().Find(sid)
   	interrupted := ok && s.runController().InterruptToSend(runID)
   	writeJSON(w, map[string]any{"interrupted": interrupted})
   }
   ```

3. `frontend/src/lib/api.ts`，放在 `subagentInterruptSend`（2741-2746）旁边：

   ```ts
     /** Esc in the conversation: stop the model call in flight so the steers
      *  queued behind it go now, inside the same turn. */
     sessionInterruptSend(sessionId: string) {
       return postJson<{ interrupted: boolean }, Record<string, never>>(
         `/chat/sessions/${encodeURIComponent(sessionId)}/interrupt-send`,
         {},
       )
     },
   ```

4. `frontend/src/composables/useChatStream.ts`：把 6131-6137 改成

   ```ts
     /**
      * Esc while messages wait on the run: the engine stops the model call in
      * flight so the steers queued behind it go now, inside the same turn — one
      * run, one clock, no "Worked for". Only when the engine has nothing to
      * interrupt does this fall back to stopping the run.
      */
     async function sendPendingSteersAfterInterrupt() {
       const sid = String(sessionId.value ?? '').trim()
       if (sid) {
         try {
           if ((await forebrainApi.sessionInterruptSend(sid)).interrupted) return
         } catch {
           // An unreachable interrupt falls through to the stop it replaces.
         }
       }
       cancel()
     }
   ```

   `cancel()` 保持原样（单选 `stream.cancel()` 的两处既有测试不变）。

5. `frontend/src/views/ChatView.vue`：**必须把"停止"和"Esc/队列打断"拆开**，现在它们共用同一个 handler
   （498、526、569 三处都指向 `interruptAndSendPendingSteers`）。

   ```ts
   async function interruptAndSendPendingSteers() {
     await sendPendingSteersAfterInterrupt()
   }
   ```

   - 495-499 `PendingInputQueuePopover @interrupt-run`：保持 `interruptAndSendPendingSteers`（队列弹层的 esc 按钮）。
   - 520-528 `ForebrainPromptTextarea @interrupt-run`：保持 `interruptAndSendPendingSteers`（输入框 Esc）。
   - 565-570 `PromptInputSubmit @stop`：改成 `cancel`（从 `useChatStream` 的解构列表里补上 `cancel`，它已在
     `useChatStream.ts:6456` 导出）。停止按钮的语义不变：**停掉 run**，不会因为队列里有消息就变成"继续跑"。

### 4.6 明确不做（已考虑并否定）

| 方案 | 否定理由 |
|---|---|
| 在 surface 里把 `cancelling run` / `Worked for` 帧吞掉，仍走取消 | 症状补丁：run 仍然结束、时钟仍重置、引擎仍分叉成两个 run，且 web 也要各写一份 |
| 在 `DispatchSurfaceTurn` 之上加一层"attempt 循环"重跑整个 turn | TUI 与 gateway 没有共用的 dispatch 层；要么新造一层引擎循环（改动远大于本方案），要么两边各写一份（违背"下沉引擎"） |
| 让子代理改走主 run 的模型调用级打断 | 子代理的 attempt 粒度是既有裁决（plan 142 / 2026-10-26 记录），本期不动 |
| 删掉 `BoundaryInterruptToSend` / `submitPendingSteersAfterInterrupt` | 仍被 SIGINT 路径使用（owner 2026-10-29 裁决：Ctrl+C 保持取消），不是死代码 |

## 5. 行为变化矩阵

| 场景 | 修复前 | 修复后 |
|---|---|---|
| 主视图 Esc + 队列有 steer，模型正在流式输出（无工具） | 取消 run → `cancelling run` → `Worked for …` → steer 作为**新一轮**发出，Working 计时归零 | 放弃这次调用 → 半截回答留在正文 → steer 以用户消息出现在同一轮 → 同一 turn 再次调用模型；**无** `cancelling run`、**无** `Worked for`、计时连续 |
| 主视图 Esc + 队列有 steer，工具正在执行 | 取消 run，工具中断，steer 下一轮 | turn 继续；未开始的调用按既有 `steerWaiting()` 语义读 not-run，steer 在下一个模型调用发出（owner 2026-10-29 裁决：队列非空即按打断处理，不再取消 run） |
| 主视图 Esc + 队列为空，模型调用在飞 | 取消 run | **不变**（引擎返回 false → 旧 fallback） |
| 主视图 Esc + 队列为空，工具调用进行中 | 取消 run | **不变**（owner 2026-10-29 裁决原文：「如果 Message queue 队列为空，工具调用还在进行中，用户按下 esc 的效果与现状保持一致」；引擎返回 false → 旧 fallback） |
| 主视图 Esc + 队列有 steer，run 停在工具审批 | 取消 run，排队消息回 composer | (owner 裁决) 不取消审批、不改 run；steer 留在队列，审批决定后恢复的那次调用立即带上它 |
| 主视图 Esc + 队列只有 follow-up（steer 为空） | 取消 run，follow-up 回 composer/下一轮 | **不变**（引擎只认 steer；`InterruptToSend` 返回 false → 走旧 fallback） |
| 主视图 Esc + 首 token 前（`withdraw`） | 撤回提交 | **不变**（`withdraw` 在插入点之前） |
| Ctrl+C / SIGINT + 队列有 steer | 取消 + `BoundaryInterruptToSend` 重发 | **不变**（owner 裁决） |
| 子代理视图 Esc | interrupt-to-send | **不变** |
| web 主视图 Esc / 队列弹层 esc | `POST /runs/:id/cancel` | `POST /chat/sessions/:id/interrupt-send`（true 即止；false 回落 cancel） |
| web 停止按钮（`PromptInputSubmit @stop`） | `cancel()` | **不变**（显式拆开后仍是 `cancel()`） |
| web 子代理视图 Esc | `interruptSubagentToSend` | **不变** |

## 6. Scope

**In scope（只许改这些）**

- `pkg/run/turn_input.go`
- `pkg/run/orchestration_llm.go`
- `pkg/run/controller.go`
- `pkg/tui/notify.go`、`pkg/tui/chat_session.go`、`pkg/tui/run.go`（只 ESC 分支）
- `pkg/gateway/api_extra.go`（一条路由）、`pkg/gateway/run_control.go`（一个 handler）
- `frontend/src/lib/api.ts`、`frontend/src/composables/useChatStream.ts`、`frontend/src/views/ChatView.vue`
- 测试：`pkg/run/turn_input_test.go`、`pkg/run/orchestration_llm_test.go`、`pkg/run/controller_test.go`、
  `pkg/tui/run_test.go`、`pkg/gateway/api_extra_test.go`、
  `frontend/src/composables/useChatStream.test.ts`、`frontend/src/views/ChatView.test.ts`
- `plans/193-main-view-esc-interrupt-to-send.md` + `plans/README.md` 一行索引

**Out of scope（看起来相关但明确不碰）**

- `pkg/run/subagent.go` 及其测试（子代理机制是参照物，不改）
- `pkg/run/supervisor.go`、`pkg/process/*`（run 生命周期不变）
- `pkg/turn/*`（`Boundary` 只在 `pkg/run`，且 SIGINT 路径继续用）
- `pkg/gateway/server.go` 的 WS `cancel_run`（仍是取消）
- TUI 的 Ctrl+C / 信号分支（`pkg/tui/run.go:3830-3840`、`4727-4750`、`5100-5110`）
- §0.2 列出的并行 WIP 文件
- web 样式/组件（`PendingInputQueuePopover.vue` 的 esc 按钮文案不改）

---

## 7. Steps（每步自带验证与预期）

> 全部在**当前工作区**执行（owner 裁决：in-tree、零 commit、改动留工作区）。不要用 worktree。
> 每条命令都在仓库根执行；测试必须显式给 `FOREBRAIN_HOME`（house rule：测试绝不继承真实 home）。

### Step 0 — 基线归因（不改任何文件）

```bash
git rev-parse --short HEAD                      # bc3339b7
git status --porcelain | wc -l                   # 43
git diff --stat -- pkg/tui/run.go pkg/tui/run_test.go
git diff -- pkg/tui/run.go | grep -n '^@@'       # 确认并行 hunk 不在 4638-4726
```

再跑 §0.3 的 drift check 表。任一条内容不符 → **STOP**。

### Step 1 — 引擎原语（`pkg/run/turn_input.go` + 单测）

改：`TurnInputRuntime` 加 `callAbort` 字段、`bindCallAbort`、`endCall`、`InterruptToSend`（按 §4.1 的行为与注释口吻）。

测试：`pkg/run/turn_input_test.go` 追加三条（文件已有 `NewTurnInputRuntime()` 的密集用例，照其风格）：

- `TestInterruptToSendReportsFalseWithNothingWaiting`：空 runtime → false；只有 follow-up → false。
- `TestInterruptToSendAbortsTheCallInFlight`：`rt.Enqueue(TurnInputModeSteer, …)`；`rt.bindCallAbort(func(){ aborted++ })`；
  `rt.InterruptToSend()` 为 true 且 `aborted == 1`；再调一次仍 true 且 `aborted == 2`（句柄未清时每次都放弃当前调用）。
- `TestInterruptToSendWithoutACallStillReportsTrue`：有 steer、未 bind → true、不 panic。

```bash
FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/run -run 'TestInterruptToSend' -count=1
```

预期：`ok  github.com/forebrain-harness/forebrain-harness/pkg/run`。

### Step 2 — 循环分支（`pkg/run/orchestration_llm.go` + 引擎集成测试）

改：按 §4.2 的 1-5 条（`errInterruptToSend`、绑定、取原因、settle-and-continue 分支、`Execute` 注释）。

测试：`pkg/run/orchestration_llm_test.go` 新增 `TestInterruptToSendStopsTheCallAndContinuesTheTurn`，
**照 `TestRetractedInFlightSteerIsResentWithoutIt`（同文件 5537-5579）的结构写**，用一个新的 fake：

```go
// interruptDuringCallLLM streams a partial answer and then blocks, so the test
// can press Esc mid-call the way a user does: the steer is queued while the
// call is running, the call is abandoned, and the same turn asks again.
type interruptDuringCallLLM struct {
	rt    *TurnInputRuntime
	q     *InputQueue
	calls [][]llm.Message
	// interrupted records that the second call observed its own cancellation.
	interrupted bool
}

func (m *interruptDuringCallLLM) Execute(ctx context.Context, msgs []llm.Message, _ []*llm.Tool) (*llm.Result, error) {
	m.calls = append(m.calls, append([]llm.Message(nil), msgs...))
	switch len(m.calls) {
	case 1:
		msg := llm.AssistantMessage(nil, llm.ToolCall{ID: "call-read", Type: llm.ToolTypeFunction,
			Function: llm.FunctionCall{Name: "read_file", Arguments: `{"file_path":"server.go"}`}})
		return &llm.Result{Message: &msg, Usage: &llm.Usage{}}, nil
	case 2:
		if sink := llm.StreamSinkFrom(ctx); sink != nil && sink.OnDelta != nil {
			sink.OnDelta("half an answer")
		}
		// The real path: the queue stamps the entry's seq and hands it to the
		// attached runtime, which is what the surface's composer does.
		if !m.q.Steer(Input{Text: "keep going", Parts: []llm.ContentPart{llm.Text("keep going")}}) {
			return nil, errors.New("steer refused while the turn was running")
		}
		if !m.rt.InterruptToSend() {
			return nil, errors.New("InterruptToSend refused a queued steer")
		}
		<-ctx.Done()
		m.interrupted = true
		return nil, ctx.Err()
	default:
		msg := llm.AssistantMessage([]llm.ContentPart{llm.Text("done")})
		return &llm.Result{Message: &msg, Usage: &llm.Usage{}}, nil
	}
}
```

断言（缺一不可）：

1. `Execute` 返回 `err == nil`（打断不是错误、不是取消）；
2. `len(inner.calls) == 3`，第三次调用最后一条是 `RoleUser` 的 `keep going`；
3. `inner.interrupted == true`；
4. `res.Message.TextContent() == "done"`；
5. `res.Session`（持久化内容）里同时有 `half an answer`（半截回答被保留，不是 retraction 的丢弃语义）与 `keep going`；
6. `q.TakeDelivered()` 只有 `keep going` 一条；
7. 通过 `llm.WithStreamSink(ctx, streamTestSink())` 提供 sink（`pkg/run/streaming_tools_test.go:859` 有现成 helper）。

```bash
FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/run -run 'TestInterruptToSend|TestRetract|TestCancelledRun' -count=1
```

预期：全绿。**先红后绿**：只写测试、不写分支时，第 1/2 条必定失败（`Execute` 返回取消错误、`len(calls)` 停在 2）。

### Step 3 — Controller 入口（`pkg/run/controller.go` + 单测）

改：按 §4.3 的 `Controller.InterruptToSend`（不加包级 helper）。测试放 `pkg/run/controller_test.go`
（该文件 122 行已有 `rt := NewTurnInputRuntime()` 的用法）：

- `TestControllerInterruptToSendReachesTheRunRuntime`：`ctl := NewController()`；
  `ctl.TrackRuntime("run-1", "s1", func(){}, rt)`；`q := ctl.SessionQueue("s1")`，`q.Steer(Input{…})`，
  `rt.bindCallAbort(func(){ aborted++ })`；`ctl.InterruptToSend("run-1")` 为 true 且 `aborted == 1`；
  `ctl.InterruptToSend("missing")` 为 false；空队列的活 run 为 false。

```bash
FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/run -run 'TestControllerInterruptToSend' -count=1
```

### Step 4 — TUI 接线（`pkg/tui`）

改（按 §4.4）：`notify.go` 接口、`chat_session.go` 实现、`run.go` 的插入点（`state.composer.Text = ""` 之前）。

测试（`pkg/tui/run_test.go`）：

1. `fakeSession`（定义见 736-763 行）加两个字段 `interruptSendMain bool`、`interruptSendMainCalls int`，
   并实现 `func (f *fakeSession) InterruptToSend(_ string) bool`（锁内自增、返回字段；照 1366-1371 的
   `InterruptSubagentToSend` 写法）。**注意保留**既有的 `interruptToSend`/`interruptSentCalls`（子代理用）。
   这一步与 §4.4 的接口改动是**同一个编译单元**：`Session` 接口加方法后，`pkg/tui` 的测试包在 `fakeSession`
   （`pkg/tui/run_test.go:1258` 是它实现 `CancelActiveRun` 的地方）补上实现前**无法编译** —— 两处一起改，
   不要分两次提交验证。
2. 改写 `TestEscInTheMainViewIsUnchanged`（5922-5955）为三段，函数名改成
   `TestEscInTheMainViewActsOnTheQueueBeforeCancellingTheRun`：
   - 第一段（withdraw）原样保留；
   - 第二段：`steer := &fakeSession{cancelActive: true, interruptSendMain: true}` + `steer.enqueueSteer(...)`，
     期望 `steer.interruptSendMainCalls == 1`、`steer.cancelCalls == 0`、`steerState.userInterrupted == false`、
     `steerState.submitPendingSteersAfterInterrupt == false`，且 `out.String()` 里**不含** `cancelling run`
     （渲染器改造成 `var out bytes.Buffer; renderer := NewRenderer(&out, &out)`，照同文件 2900-2902 的写法）；
   - 第三段：`interruptSendMain: false` + `cancelActive: true` + 队列里有 steer → 走旧 fallback，
     `cancelCalls == 1`、`submitPendingSteersAfterInterrupt == true`、`userInterrupted == true`。
3. 新增 `TestEscInTheMainViewWithoutSteersStillCancelsTheRun`：两条子用例，都要求 `cancelCalls == 1`、
   `userInterrupted == true`，且 `out.String()` 含 `cancelling run`：
   - **队列为空 + 模型调用在飞**（`interruptSendMain: false`，引擎必然返回 false）；
   - **队列为空 + 工具调用进行中**（同上；owner 2026-10-29 明确要求这一条与现状一致）。
   这条用例是 owner 裁决的守卫：任何人日后把"空队列"也改成不取消，它必须变红。

```bash
FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -run 'TestEsc' -count=1
```

预期：全绿，且 `grep -c 'InterruptToSend' pkg/tui/*.go` ≥ 3。

### Step 5 — 网关路由（`pkg/gateway`）

改：`api_extra.go` 一条路由 + `run_control.go` 一个 handler（按 §4.5 的 1-2）。

测试 `pkg/gateway/api_extra_test.go`，照 `TestSubagentViewEndpointsRefuseAForeignConversation`（5873-5880）
与 `subagentViewRequest`（5836-5844）的 params-context 写法：

- `TestSessionInterruptSendReportsTheEngineAnswer`：`s, _ := newSubagentViewTestServer(t)`；在 **`s.runController()`**
  返回的那个控制器上 `TrackRuntime("run-1", sid, func(){}, rt)`（不要写 `s.Runner.Control`，两者在测试服务器里未必是同一对象），
  `rt.Enqueue(TurnInputModeSteer, …)` + `bindCallAbort`；
  `POST` 后断言 200 且 body 为 `{"interrupted":true}`、abort 已触发；没有 run 的会话返回 `{"interrupted":false}`。
- 复用同一 helper 断言**外来会话** 404（`sessionOwned`）。

```bash
FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/gateway -run 'TestSessionInterruptSend|TestSubagentViewEndpoints' -count=1
```

### Step 6 — web 接线（`frontend`）

改：`lib/api.ts` 新增 `sessionInterruptSend`；`useChatStream.ts` 的 `sendPendingSteersAfterInterrupt` 变 async 并先问引擎；
`ChatView.vue` 拆开 `@stop`（→ `cancel`）与两处 `@interrupt-run`（→ `interruptAndSendPendingSteers`）。

测试：

- `frontend/src/composables/useChatStream.test.ts`：新增一条 `it`
  「Esc interrupts the run before cancelling it」——`vi.spyOn(forebrainApi,'sessionInterruptSend').mockResolvedValue({interrupted:true})`
  + `vi.spyOn(forebrainApi,'runCancel')`，`await stream.sendPendingSteersAfterInterrupt()` 后断言
  `runCancel` **未**调用；再一条返回 `{interrupted:false}` 时 `runCancel` 被调用一次（带当前 `activeRunId`）。
  既有的两处 `stream.cancel()` 用例（3910、3952）**不改**（stop 仍走 cancel）。
- `frontend/src/views/ChatView.test.ts`：`sendPendingSteersAfterInterrupt: vi.fn()`（108 行）改成可观测的 fake，
  断言点 `@stop` 时调用的是 `cancel` 而不是 `interruptAndSendPendingSteers`（用 `PromptInputSubmit` 的 stop 触发，
  照该文件既有的组件挂载写法）。

```bash
cd frontend && pnpm typecheck
cd frontend && pnpm vitest run src/composables/useChatStream.test.ts src/views/ChatView.test.ts
```

预期：`vue-tsc --noEmit` 无输出；两个 spec 全绿。

### Step 7 — 包级回归 + vet

```bash
CGO_ENABLED=1 go build ./...
FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/run ./pkg/tui ./pkg/gateway -count=1
go vet ./...
```

预期：三包 `ok`；`go vet` 无输出。若 `pkg/tui` 出现与并行 WIP 相关的失败，先用
`git stash list`/`git diff` 归因到并行改动（不得回滚对方改动），只对**本计划引入**的失败做修复。

### Step 8 — 真机验收（必做；测试绿 ≠ 真机可用）

1. 构建（漏 `-tags fts5` 会以 `no such module: fts5` 退出，那是工具链错误不是产品 bug）：

   ```bash
   go build -tags fts5 -o ./build/bin/forebrain ./cmd/forebrain
   ```

2. TUI（假 provider，隔离 home 与隔离项目目录；配方见 `.claude/skills/run-forebrain/SKILL.md` 与
   `scripts/acceptance/driver.sh`、`scripts/acceptance/fake_provider.py`）：
   - tmux 起 TUI → 让假 provider 输出一段**很长**的流式回答（这样 Esc 时有调用在飞）；
   - 回答期间在 composer 打第二条消息回车（steer 入队，界面出现排队预览）；
   - 按 Esc，逐屏 `tmux capture-pane`：
     - **断言 A**：出现 steer 的用户消息，且没有 `cancelling run`；
     - **断言 B**：没有 `Worked for …` 行；
     - **断言 C**：`Working (… )` 的耗时在 Esc 前后**连续增长**（两次 capture 的数值单调递增，不回 0）；
     - **断言 D**：模型在同一个 run 里回答了这条 steer；
     - **断言 E（本轮最大风险，必须专门看）**：Esc 前已经流出的半截回答**原样留在正文里**，作为**独立的一块**，
       它下面是 steer 的用户消息，再下面是新的回答 —— 三段不能粘连、半截回答不能被后面的最终答案覆盖
       （引擎用 `OnTextBlockDone` 封口，见 §4.2；渲染侧既有先例是 `pkg/tui/reducer.go:5343`
       `WithdrawDeadStreamBlocks`，这里**不能** withdraw，只能封口）。
3. 回归（同一轮会话）：
   - **队列为空 + 模型正在流式输出**，Esc → 仍然 `cancelling run` + `Worked for`（旧语义）；
   - **队列为空 + 一个长工具正在执行**，Esc → 仍然立刻取消（owner 2026-10-29 裁决；断言出现 `cancelling run`）；
   - Ctrl+C → 仍然取消。
4. web：gateway + 假 provider，浏览器里重复 2 的流程（主对话 Esc）：
   - **断言 F**：没有新的 `Worked for` 行；Working 计时连续；steer 出现在同一轮里；
   - **断言 G**：半截回答仍是独立的一条 assistant 消息，steer 的用户消息在它之后、新回答之前（同断言 E）；
   - **断言 H**：点「停止」按钮仍然停掉 run（`@stop` 已与 Esc 拆开）。
   若时间允许，用 `FOREBRAIN_E2E_SPEC` 跑一条覆盖主对话 Esc 的 spec（本期没有现成 spec，
   优先保证 1-4 的手动取证，不要为此新写一个不稳定的 e2e）。

### Step 9 — 收口

- 复制本文件为 `plans/193-main-view-esc-interrupt-to-send.md`，在 `plans/README.md` 追加一行索引
  （只追加，不动并行会话的条目），状态 `TODO`。
- 计划文件回填「验收记录」（场景 → 取证，含截图/`capture-pane` 摘录）。
- `git status --porcelain` 核对改动范围 == §6 In scope（外加并行 WIP 原样）。
- 报告：根因链、改动清单、验证证据、遗留限制。**零 commit**。

---

## 8. Done criteria（机器可查）

1. `grep -c 'func (c \*Controller) InterruptToSend' pkg/run/controller.go` = 1；
   `grep -c 'func (r \*TurnInputRuntime) InterruptToSend' pkg/run/turn_input.go` = 1。
2. `grep -c 'errInterruptToSend' pkg/run/orchestration_llm.go` ≥ 3（定义 + 绑定 + 判定）。
3. `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/run -run 'InterruptToSend' -count=1` 绿。
4. `... ./pkg/tui -run 'TestEsc' -count=1` 绿，且该测试显式断言 `cancelCalls == 0` 与"输出不含 `cancelling run`"。
5. `... ./pkg/gateway -run 'TestSessionInterruptSend' -count=1` 绿。
6. `go vet ./...` 无输出；`cd frontend && pnpm typecheck` 无输出；两个 vitest spec 绿。
7. 真机断言 A/B/C/D/E（TUI）与 F/G/H（web）全部取证通过，证据写进计划文件。
8. `grep -c 'Title: "cancelling run"' pkg/tui/run.go` 与改前一致（旧路径没被删）。
9. `git diff --stat` 里不出现 §6 Out of scope 的任何路径。

## 9. STOP conditions

1. 发现**主 run 的模型调用并不经过** `toolOrchestrationLLM.Execute`（`pkg/run/runner.go:1485` 是唯一产品装配点；
   若有别的 agent kind 走独立循环）→ 停，报告哪条路径绕过了编排层，不要为它再造一套打断机制。
2. ESC 时刻拿不到 run 的 runtime（`Controller.Find` 找不到、`q.Runtime() == nil`），或审批恢复路径用的不是
   `q.Runtime()`（核对 `pkg/gateway/approval.go:625`、`pkg/gateway/api_extra.go:4466`）→ 停，报告；
   不许新增第二条"从 session 反查 runtime"的路径。
3. Step 2 的测试在两轮编辑后仍不绿 → 停，重新评估是不是把 turn 边界语义混进了调用边界。
4. 真机看到半截回答被渲染两次（流式块 + flush 块重复），或出现空的 assistant 卡 → 停，附 `capture-pane` 原文。
5. 并行会话已经改到同一批 hunk（§0.2、§0.3 复查不符）→ 停。
6. 任何"为了让界面看起来对"而需要在 surface 里吞事件/帧的想法 → 停，回到引擎。
7. 半截回答的封口做不干净（TUI 的 live 块没封口、或被最终答案覆盖；web 上两段回答粘连成一条）→ 停，
   附 `capture-pane` / 截图。**不许**改用 `WithdrawDeadStreamBlocks`（那是 retry 丢弃文本的语义，
   会把用户已经看到的半截回答删掉）。

## 10. Maintenance notes

- **新增模型调用路径的规矩**：凡是走 `toolOrchestrationLLM.Execute` 的调用，前面 `bindCallAbort`、返回后 `endCall`。
  目前只有该循环一处；以后若有人再包一层 LLM 并自己发请求，必须同样绑定，否则 Esc 打不断它。
- **控制器要用"持有这些 run 的那个"**：产品里 `ChatSession.tuiControl`、`Runner.Control`、`Server.runController()`
  都是 `env.Control` 同一个对象；但测试/无 env 场景下 `tuiControlLocked()` 会新建一个，所以 TUI 侧必须按
  `CancelActiveRun` 的写法（`s.tuiControlLocked()` + `s.tuiRunIDLocked()`）查找，不要从 `s.runner()` 取 `Control`。
- **runtime 的作用域**是"一次 execution"：主 run 与每个子代理各自一个 runtime（`WithTurnInputRuntime` 分别注入），
  所以主视图的打断永远不会打到子代理的调用，反之亦然。改动这条链时不要跨 execution 复用 runtime。
- **surface 不做判定**：TUI / web 只转发 `InterruptToSend` 的 bool，不自己数队列、不自己判断要不要取消。
  任何"surface 里 if 队列非空 then 不取消"的写法都是这次被否定的症状补丁。
- `BoundaryInterruptToSend` / `submitPendingSteersAfterInterrupt`（`pkg/tui/run.go:4038-4062`）现在只服务
  Ctrl+C/SIGINT 路径；将来若 owner 决定把信号也统一成 interrupt-to-send，那才是删除它们的时机。
- 评审重点：① Rollback 必须在 append 半截回答**之前**；② 原因必须在 `abandonCall(nil)` **之前**读；
  ③ 新分支不得返回错误（返回即意味着 run 结束）；④ 网关 `@stop` 与 `@interrupt-run` 必须拆开；
  ⑤ 打断前必须 `OnTextBlockDone` 封口，否则 TUI 的 `flushAssistantFinal` 回退替换会把半截回答覆盖掉；
  ⑥ `InterruptToSend` 在队列为空时必须返回 false（owner 硬要求：空队列 Esc = 现状）。
- **文本块边界是这个改动的第二个不变量**：一次模型调用被中途放弃时，"它的文本块结束了"必须显式告诉 surface
  （`OnTextBlockDone`），否则 live 块永远不 Final。以后任何"放弃一次调用但保留其输出"的新路径都要照做。

---

## Implementation Status

状态：**已执行**（2026-10-29，主树 `dev` in-tree、零 commit；改动留在工作树等 owner 审阅）。

### 落地位置

| Step | 落点 | 结果 |
|---|---|---|
| 1 | `pkg/run/turn_input.go`：`callAbort` 字段 + `bindCallAbort`/`endCall`/`InterruptToSend`；`pkg/run/turn_input_test.go` 4 条用例 | done |
| 2 | `pkg/run/orchestration_llm.go`：`errInterruptToSend`、每次调用前 `bindCallAbort`、返回后**先读 cause 再** `abandonCall(nil)`+`endCall`、settle-and-continue 分支、`Execute` 文档段；`pkg/run/orchestration_llm_test.go` 的 `interruptDuringCallLLM` + `TestInterruptToSendStopsTheCallAndContinuesTheTurn` | done |
| 3 | `pkg/run/controller.go`：`Controller.InterruptToSend`；`pkg/run/controller_test.go` 一条（空队列 false／有 steer true 且 abort 命中／未知 run false／已结束 run false） | done |
| 4 | `pkg/tui/notify.go` 接口、`pkg/tui/chat_session.go` 实现（`tuiControlLocked`+`tuiRunIDLocked`）、`pkg/tui/run.go` 插入点（清 `composer.Text`/`holdComposer` 之后、`markUserInterrupt` 之前）；`pkg/tui/run_test.go` 重写 Esc 用例 + 空队列守卫 | done |
| 5 | `pkg/gateway/api_extra.go` 一条路由、`pkg/gateway/run_control.go` `handleSessionInterruptSend`；`pkg/gateway/api_extra_test.go` 两条（走真实路由表） | done |
| 6 | `frontend/src/lib/api.ts` `sessionInterruptSend`、`useChatStream.ts` 先问引擎再回落 `cancel()`、`ChatView.vue` 拆开 `@stop` 与两处 `@interrupt-run`；两条 spec | done |
| 7 | 三包回归 + `go vet` | done（见下） |
| 8 | 真机验收 | TUI A–E 全部取证；web 浏览器级 F/G/H **未做**（见「未做」） |
| 9 | 本文件归档 + `plans/README.md` 索引行 | done |

### 与计划的偏差（均为非承重细节，意图/范围/公开行为/done criteria 不变）

1. **§4.2 分支新增两个 sink 的收尾**（`adoptionSink.take()` + `reminderSink.discard()`，与 retraction 分支 590-636 同款）。
   承重理由：若被打断的那次调用里正好完成了 mid-turn 压缩，sink 里会留着一份 checkpoint；下一次**成功**调用在 664 行会用它整体替换 session，
   把「半截回答 + 刚 append 的 steer」一起丢掉（等于吞掉用户消息）。计划片段没写这两行，属遗漏而非取舍。
2. **`interruptedToSend` 条件加 `ctx.Err() == nil`**，与 retraction 分支自身的守卫同构：run 取消与 Esc 同一瞬间落地时，仍走取消路径结束 turn。
3. **半截回答仅在真有文本或工具调用时 append**（空 assistant 消息不再进入 session）。
4. **TUI 用例的「不含/含 `cancelling run`」断言读 view-model**：`paintViewportLocked` 在非 viewport 模式直接返回，
   `NewRenderer(&out,&out)` 在单测里拿不到字节。改为读 `renderer.vm.blocks` 的 `FrameStatus` 标题（helper `mainViewStatusLines`），
   断言的仍然是「有没有画这一帧」。
5. **`fakeSession` 的新字段追加在结构体末尾**：插在 `interruptSentCalls` 后会触发 gofmt 重排并行会话正在改的 12 行字段块，diff 归因会失真。
6. **引擎侧用例写 4 条**（计划 3 条）：多一条 `TestInterruptToSendSeesASteerItsCallIsCarrying` 钉住「inflight 里未结算的 steer 也算等待中」。
7. **网关用例走真实路由表**（`NewRestServer` + `AttachExtraRoutes` + `ServeHTTP`，照 `subagents_api_test.go` 的 `do` 写法），
   而不是直接调 handler：这样路由注册丢失会让用例变红，并补了一条 405 断言。
8. Step 3 用例用计划给的 `TrackRuntime` + `SessionQueue` 形状，未另写第二条反查路径。

### 先红后绿

把 §4.2 的新分支临时改成 `if false && …` 后：
`TestInterruptToSendStopsTheCallAndContinuesTheTurn` 报 `an interrupted call ended the turn: context canceled`，
`len(calls)` 停在 2 —— 即「没有这条分支就是旧的取消语义」。随后用备份还原并 `shasum -c` 校验文件一致。

### 验证结果（全部本人复跑）

```
FOREBRAIN_HOME=$(mktemp -d) CGO_ENABLED=1 go test -tags fts5 ./pkg/run -run InterruptToSend -count=1     ok  1.257s
... ./pkg/tui    -run TestEsc               -count=1     ok  1.184s
... ./pkg/gateway -run TestSessionInterruptSend -count=1 ok  1.700s
... ./pkg/run ./pkg/tui ./pkg/gateway -count=1            ok 41.5s / 78.2s / 28.3s
CGO_ENABLED=1 go build ./...                              ok
go vet ./...                                              无输出
cd frontend && pnpm typecheck                             无输出
cd frontend && pnpm vitest run useChatStream.test.ts ChatView.test.ts   2 files / 237 tests passed
grep -c 'func (c *Controller) InterruptToSend' pkg/run/controller.go     1
grep -c 'func (r *TurnInputRuntime) InterruptToSend' pkg/run/turn_input.go  1
grep -c 'errInterruptToSend' pkg/run/orchestration_llm.go                4
grep -c 'Title: "cancelling run"' pkg/tui/run.go                          8（与改前一致，旧路径未删）
```

### 真机验收（TUI，`zhipuai/glm-5.3-flash` · **low**，假 provider `drip` 1.0s/词，隔离 home）

配置（隔离 home 的 `forebrain.yaml`，owner 指定模型）：

```yaml
      - provider: zhipuai
        model: glm-5.3-flash
        params: '{"reasoning":{"effort":"low"}}'
        api_key: ${FAKE_LLM_KEY}
        base_url: http://127.0.0.1:8791
```

footer 实证生效：`zhipuai/glm-5.3-flash · low    100%/1M`。

**A/B/C/D/E 取证（同一次会话）**

| 时刻 | `capture-pane` 摘要 |
|---|---|
| 队列已入队、调用仍在流（S1） | `● alpha one beta two gamma three` / `Working (5s • esc to interrupt)` / 预览块 `↳ keep going` |
| 按 Esc 后 2s（S2） | `● alpha one beta two gamma three`（半截回答独立成块）→ `› keep going`（用户消息卡）→ `● alpha one`（新一轮在流）/ `Working (7s • …)` / **无** `cancelling run` / **无** `Worked for` |
| Esc 后 12s（S3） | 第二轮回答已流完 `… sigma eighteen`，仍只有 Working 行，无 `Worked for` |
| 整轮真正结束（S4） | 仅一行 `─ Worked for 41s · first token 0.0s · 19:26 ─`（= 一次 run 的收尾，不是被打断的收尾） |

- **A** 成立：`› keep going` 以用户消息出现在正文，全程无 `cancelling run`。
- **B** 成立：Esc 后到本轮真正结束之前没有 `Worked for …`；结束时只出现一次。
- **C** 成立：`Working` 计时 5s → 7s → … → 最终 `Worked for 41s` 覆盖整轮，未归零。
- **D** 成立：同一个 run 内模型回答了这条 steer。
- **E** 成立：半截回答 `alpha one beta two gamma three` 原样留在正文，作为独立一块位于 steer 之上，没有被第二轮回答覆盖/替换。
- 落库（durable 侧）：`fb_messages` = `1 user first question` / `3 assistant alpha one beta two gamma three` / `4 user keep going` / `5 assistant …`；
  `fb_runs` 只有 **1** 行且 `status=done` —— 全程一个 run，没有「取消 + 新一轮」。
- provider 日志：2 条 streaming `REQUEST`；第一条以 `BrokenPipeError` 结束（被放弃的那次调用断开连接，预期）。

**回归（同一隔离环境）**

| 场景 | 结果 |
|---|---|
| 队列为空 + 模型正在流式输出 + Esc | `cancelling run` + `Worked for 3s`（旧语义未变） |
| 队列为空 + 长工具正在执行（`sleep 60`）+ Esc | `cancelling run`、工具卡 `context canceled`、`Worked for 1s`（owner 裁决①：与现状一致） |
| Ctrl+C + 队列有 steer | **未能复现「仍然取消」**；见「观察」 |

### 观察（不在本计划范围，未修改）

- **单次 Ctrl+C 在该构建下没有取消 run**：streaming 中按一次 Ctrl+C，2s/7s 后 run 仍 `running`，无 `cancelling run`。
  该分支与 HEAD **逐字节相同**（`git diff -- pkg/tui/run.go | grep -c hotkeyInterrupt` = 0，diff 只有 4686/4695 两处 ESC hunk），
  按计划 §6「Ctrl+C／信号分支」为 out of scope，未动；建议另立一条定位（`run.go:4740-4763` 的 `CancelActiveRun()` 为何返回 false）。
- **排队 steer 后同一文本仍留在 composer 里**：入队瞬间（Esc 之前）就可见，与本改动无关；整轮结束时**没有**被自动重发（`fb_messages` 无重复行、无第二个 run）。
  是否属于「入队后应清空」的既有缺陷，建议单独确认。

### 未做

- **web 浏览器级 F/G/H**（step 8.4）：本会话没有可用的浏览器自动化驱动，未开浏览器取证。
  已覆盖的替代证据：①网关用例走**真实路由表**（路由注册、`{"interrupted":…}`、外来会话 404、GET 405）；
  ②`useChatStream` 用例断言「引擎说 true 就不调 `runCancel`，说 false 才调一次」；③`ChatView` 用例断言「停止按钮调 `cancel`，不调 `sendPendingSteersAfterInterrupt`」。
  引擎侧与 TUI 是同一段代码（`Controller.InterruptToSend`），已在真机验证。
- 计划 §5 矩阵「队列有 steer + run 停在工具审批」未做真机取证：审批面板自己消费 Esc（`uiPanelActive` 分流），主视图的 Esc 分支到不了；
  引擎侧规则由 `TestInterruptToSendWithoutACallStillReportsTrue` / `TestControllerInterruptToSendReachesTheRunRuntime` 钉住（无调用在飞时仍返回 true，不回落取消）。
