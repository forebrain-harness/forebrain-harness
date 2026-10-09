# Plan 003: 队列里显示的 steer 在模型开始回答前随时能撤回、清空时一并撤走，web/gateway 与 TUI 一致

> **Executor instructions**: Follow this plan step by step. Run every
> verification command and confirm the expected result before moving to the
> next step. If anything in the "STOP conditions" section occurs, stop and
> report — do not improvise. When done, update the status row for this plan
> in `plans/README.md`.
>
> **Drift check (run first)**:
> `git diff --stat 71c0449..HEAD -- pkg/run/turn_input.go pkg/run/turn_input_test.go pkg/run/orchestration_llm.go pkg/run/orchestration_llm_test.go pkg/run/compaction.go pkg/run/compaction_test.go pkg/run/llm_middleware.go pkg/run/subagent.go pkg/run/subagent_test.go pkg/event/run_events.go pkg/gateway/run_control.go pkg/gateway/server.go pkg/gateway/api_extra.go pkg/gateway/run_control_test.go pkg/gateway/api_extra_test.go frontend/src/composables/useChatStream.ts frontend/src/composables/useChatStream.test.ts frontend/src/components/ForebrainPromptTextarea.vue frontend/src/components/ForebrainPromptTextarea.test.ts frontend/src/views/ChatView.vue frontend/src/lib/forebrainGatewayRuntime.ts .claude/skills/run-forebrain/fake_provider.py .claude/skills/run-forebrain/SKILL.md`
> If any in-scope file changed since this plan was written, compare the
> "Current state" excerpts against the live code before proceeding; on a
> mismatch, treat it as a STOP condition.

## Status

- **Priority**: P1（owner 报告的严重体验 bug：队列里看得见的消息撤不回，过一会自己发出去）
- **Effort**: L
- **Risk**: MED（引擎并发结算 + 中途压缩切分 + web run 投影中途换气泡；全部有单测、`-race` 与前端 vitest 兜底）
- **Depends on**: none
- **Category**: bug
- **Planned at**: commit `71c0449`, 2026-10-09
- **Owner 裁决（2026-10-09）**：压缩窗口选「压缩不吞未送达 steer」——中途压缩只对 steer 之前的历史做 checkpoint，steer 原样接在 checkpoint 之后；压缩跑在不受撤回影响的 ctx 上。
- **Owner 裁决（2026-10-09）**：「清空队列」（`InputQueue.Discard`）对在途 steer 的同类缺口**必须并入本期一起修**（Step 5）。
- **Owner 裁决（2026-10-09）**：web/gateway 的同类问题**必须并入本期一起修**（Step 6–10）；其中「web 主会话里送达的 steer 不进实时对话」选「实时拆成独立气泡」——与 TUI 和刷新后的历史回放形态一致。

## Why this matters

TUI 运行中提交的消息会进入队列成为 steer，显示在 composer 上方的队列预览里，Shift+← 可以把最新一条撤回到 composer 编辑。但 steer 在**工具边界**会被 runtime 取走、拼进下一次模型请求；从这一刻起，到模型吐出**首个输出事件**（此时才 Commit、消息才离开预览）之间，消息仍显示在队列里，`Recall` 却必然失败——按键无响应，等模型首 token 到达后消息就自动作为已发送消息出现在 transcript。这个窗口就是下一次请求的首 token 延迟（长上下文 / thinking 模型常达数秒到十几秒；若这次请求恰好触发中途自动压缩，还要再加上整段压缩时间）。

同一个窗口也让「清空队列」失效：切换会话时 TUI 调 `InputQueue.Discard` 丢弃旧会话的排队消息，但它只撤得回 runtime `pending` 里的 steer，对已被取走、尚未 Commit 的在途 steer 只清掉预览——旧 run 照样把它交给模型，Commit 时队列里已找不到它，于是**模型回答了一条从未出现在 transcript 里的消息**，这正是该调用点注释（`pkg/tui/run.go:1697-1702`）明确要防的情况。另外 runtime 已 detach 时 `Discard` 根本不数 steer，「N queued messages not carried into the new session」提示会少报。

修复后的不变式：**只要 steer 还显示在队列里（尚未 Commit），撤回就立即成功，清空队列也一定把它撤走并计数**；被撤回的消息模型永远看不到——承载它的在途请求被中止，引擎去掉这条后自动重发；中途压缩的 checkpoint 永远不包含未送达的 steer（顺带修掉「压缩后请求失败、steer 回滚重发 → checkpoint 里一份、重发一份」的潜伏重复 bug）。TUI、subagent 视图、gateway 三个撤回入口都走同一个 `InputQueue.Recall`，引擎层修一处全部生效。

web 端在此之外还有三处自己的同类缺口（见「web / gateway 的同类问题」）：subagent 视图的队列预览只在 HTTP 操作后才推送，steer 被送达或执行结束后预览不更新，于是已送达的消息一边显示成用户消息、一边还挂在队列里，按撤回必然失败（刷新后从事件日志回放的仍是这份旧预览）；web 主会话里 steer 被送达后从队列消失，却不会画进实时对话（TUI 会画成一条 you 消息），要刷新才看得到，gateway 会话队列的 delivered 列表也因此只增不减；web 的 Shift+← 只在本页自己发起的 run 流式期间生效，主会话空闲时的 subagent 视图、或刷新后仍在进行的 run，队列明明显示着按键却无效。修复后 web 与 TUI 遵守同一条不变式：**队列里看得见的就撤得回；送达的就作为用户消息出现在模型收到它的位置**。

## Current state

（全部摘录已在 commit `71c0449` 逐行核实。）

### 涉及文件

- `pkg/run/turn_input.go` — steer runtime（`TurnInputRuntime`）、在途交付（`SteerDelivery`）、会话队列（`InputQueue`，含 `Recall`）。
- `pkg/run/orchestration_llm.go` — 工具编排循环：在工具边界 / 无工具调用的最终回答处取走 steer、发起下一次模型调用、在首个输出事件 Commit。
- `pkg/run/llm_middleware.go` — `recoverableLLM`：每次模型调用前的中途自动压缩、上下文溢出时的被动压缩。
- `pkg/run/compaction.go` — `compactionAdoptionSink`（压缩结果交回编排循环）等压缩辅助。
- 测试：`pkg/run/turn_input_test.go`、`pkg/run/orchestration_llm_test.go`、`pkg/run/compaction_test.go`。
- 真机验收工具：`.claude/skills/run-forebrain/fake_provider.py`、`.claude/skills/run-forebrain/SKILL.md`。

### 根因事实链

1. **入队双写**（`pkg/run/turn_input.go:533-534`，`InputQueue.Steer`）：消息进 `q.steers`（队列预览的数据源），同时 `q.rt.enqueueEntry(TurnInputEntry{Mode: TurnInputModeSteer, Parts: parts, Seq: in.Seq})` 进 runtime 的 `pending`。两边用同一个 `Seq`（队列共享时钟，从 1 开始）对应。
2. **工具边界取走**（`pkg/run/orchestration_llm.go:328-335`）：`BeginSteerDelivery` → `takeSteers`（`turn_input.go:66-91`）把 steer 从 `rt.pending` 移出，追加进 session，紧接着的那次 `w.inner.Execute` 带着它发出。无工具调用的最终回答处（`orchestration_llm.go:245-256`）同理。
3. **Commit 才离开队列**：只有首个输出事件（`withSteerDeliveryResponseStart`，`orchestration_llm.go:346-363`，provider 在转发第一个输出事件前调 `OnResponseStarted`）或非流式调用成功返回（`orchestration_llm.go:201`）才 `Commit` → change hook `steersDelivered`（`turn_input.go:431-456`）把它移出 `q.steers`。在此之前 `q.steers` 仍持有它，`Preview()` 照常显示（TUI 的预览直接读 `q.Preview()`：`pkg/tui/run.go:2201-2221`）。
4. **Bug 所在**（`pkg/run/turn_input.go:630-643`，`InputQueue.Recall` 的 steer 分支）：

```go
		case laneSteer:
			// When a runtime is attached, retract from it first: a failed
			// retraction means the run already drained the steer for a model
			// call — and since the runtime drains FIFO, every older steer is
			// gone too. When the runtime is detached, the steer never left the
			// queue (delivery settles inside the run loop before the run can
			// end, and Detach runs after that), so it can be recalled directly.
			if q.rt != nil {
				if _, ok := q.rt.RetractLastSteer(); !ok {
					skipSteers = true
					continue
				}
			}
			laneEntries = &q.steers
```

`RetractLastSteer`（`turn_input.go:214-230`）只查 `r.pending`；在途交付里的 steer 已不在 `pending`，于是撤回失败、整条 steer 车道被跳过。队列里没有别的消息时 `Recall` 返回 false，TUI 的 `restoreLatestQueuedEditableSubmission`（`pkg/tui/run.go:2787-2797`）什么也不做。
5. **现有测试把这个行为钉死了**：`pkg/run/turn_input_test.go:166-185` 子测试 `"unretractable steer is skipped"`（`BeginSteerDelivery` 之后 `Recall` 必须跳过该 steer）。本计划会按 owner 的新需求改写它。
6. **压缩在窗口之内**：包装链从外到内是 orchestration → guardrails → `recoverableLLM` → reminder 包装 → provider（`pkg/run/runner.go:1051`、`1068`、`1279`）。`recoverableLLM.Execute`（`llm_middleware.go:220-231`）在发请求前做中途自动压缩；压缩的 `TryCompactOnMessages`（`pkg/assembly/service.go:291-329`）会**落库 checkpoint**。当前压缩输入是整个 session（含在途 steer），所以 checkpoint 会把未送达的 steer 卷进去。

三个撤回入口都调 `InputQueue.Recall`：`pkg/tui/run.go:2791`（主会话）、`pkg/run/subagent.go:1935`（subagent 视图）、`pkg/gateway/run_control.go:130`（web `edit_last`）。

### 「清空队列」的同类缺口

`pkg/run/turn_input.go:737-763`（`InputQueue.Discard`）：

```go
// Discard empties every lane and returns how many messages were dropped.
// Pending steers are retracted from the attached runtime first: clearing
// only the queue would leave the runtime free to hand them to the model.
// Steers already delivered cannot be retracted and are not counted — the
// model has them, so they are not lost.
func (q *InputQueue) Discard() int {
	if q == nil {
		return 0
	}
	q.mu.Lock()
	dropped := 0
	for len(q.steers) > 0 && q.rt != nil {
		if _, ok := q.rt.RetractLastSteer(); !ok {
			break
		}
		q.steers = q.steers[:len(q.steers)-1]
		dropped++
	}
	dropped += len(q.rejected) + len(q.followUp)
	q.steers, q.rejected, q.followUp, q.delivered = nil, nil, nil, nil
	hook := q.hook
	q.mu.Unlock()
	if hook != nil {
		hook()
	}
	return dropped
}
```

- 调用方：切换会话 `pkg/tui/run.go:1697-1709`（`discardQueuedInput` + `discardSubagentQueues`，随后渲染 `"queued input discarded"` 提示，`pkg/tui/run.go:1728-1734`）；subagent 队列经 `run.DiscardSubagentInput`（`pkg/run/subagent.go:1961-1969`）。
- 缺口 1（在途）：`RetractLastSteer` 只查 `pending`，在途 steer 撤不回、循环 `break`，最后 `q.steers = nil` 把它从预览抹掉——模型仍会回答它，`steersDelivered` 在 `q.steers` 里找不到它，不渲染进 transcript。
- 缺口 2（detached）：`q.rt == nil` 时循环不执行，steer 被清掉却不计数。按 plan 001 已确立的语义（detach 后留在队列里的 steer 从未送达），它们应计入丢弃数。
- 现有测试 `TestInputQueueDiscardCountsWhatItDrops` 第二段（`pkg/run/turn_input_test.go:335-346`）断言「在途 steer 不计入丢弃数」——本计划改写它。
- `TakeAll` / `Next` / `consumeSteersLocked` 不改：`Next` 只在 run 结束后调用（在途交付已结算）；`TakeAll` 只在 Esc 撤回刚提交消息的窗口里调用（`pkg/tui/run.go:2872`、`pkg/run/subagent.go:770`），该窗口在 turn 首个输出事件之前，此时还没有任何工具边界，不可能存在在途交付。

### 关键现状代码摘录

`pkg/run/turn_input.go:29-33`：

```go
type TurnInputRuntime struct {
	mu       sync.Mutex
	pending  []TurnInputEntry
	onChange []func(delivered []TurnInputEntry)
}
```

`pkg/run/turn_input.go:135-208`（`SteerDelivery` 及其方法，摘要）：

```go
type SteerDelivery struct {
	rt      *TurnInputRuntime
	entries []TurnInputEntry
	mu      sync.Mutex
	settled bool
}

func (r *TurnInputRuntime) BeginSteerDelivery(ctx context.Context) *SteerDelivery {
	entries := r.takeSteers(ctx)
	if len(entries) == 0 {
		return nil
	}
	return &SteerDelivery{rt: r, entries: entries}
}

func (d *SteerDelivery) Commit() bool { /* d.mu: settled check → settled=true; then d.rt.notifyDelivered(d.entries) */ }
func (d *SteerDelivery) Rollback() bool { /* d.mu: settled check → settled=true; then d.rt.restoreSteers(d.entries) */ }
```

`pkg/run/orchestration_llm.go:164-201`（编排循环头）：

```go
	var inFlightSteers *SteerDelivery
	sessionBeforeSteers := 0
	for {
		ctx = w.state.ContextWithRuntimeSessionMode(ctx)
		// ...snapshot comment...
		partialCapture.Set(session)
		callCtx := withSteerDeliveryResponseStart(ctx, inFlightSteers)
		res, err := w.inner.Execute(callCtx, session, tools)
		if err != nil {
			// ...comment...
			if inFlightSteers != nil {
				rolledBack := inFlightSteers.Rollback()
				inFlightSteers = nil
				if rolledBack {
					session = session[:sessionBeforeSteers]
					partialCapture.Set(session)
				}
			}
			return res, err
		}
		// A successful non-streaming call has no response-start event, so commit
		// here. ...
		inFlightSteers.Commit()
		inFlightSteers = nil
```

`pkg/run/orchestration_llm.go:232-283`（无工具调用分支：最终回答处取走 steer 或结束 turn）：

```go
		if res == nil || res.Message == nil || len(res.Message.ToolCalls) == 0 {
			if sessionHasToolExecution(session) && isEmptyAssistantResult(res) {
				return nil, fmt.Errorf("empty assistant response after tool execution")
			}
			// ...comment...
			if res != nil && res.Message != nil {
				if delivery := TurnInputRuntimeFromContext(ctx).BeginSteerDelivery(ctx); delivery != nil {
					session = append(session, *res.Message)
					noteResponseCompleted()
					sessionBeforeSteers = len(session)
					for _, entry := range delivery.Entries() {
						session = append(session, llm.UserMessage(entry.Parts...))
					}
					inFlightSteers = delivery
					partialCapture.Set(session)
					continue
				}
			}
			resolved, stopErr := w.applyStopHooks(ctx, session, res)
			if stopErr != nil {
				return nil, stopErr
			}
			if !sameUsage(latestUsage, resolved) {
				accumulateUsage(&totalUsage, usageCopy(resolved))
			}
			attachTotalUsage(&totalUsage, resolved)
			// ...long comment "Propagate the full orchestration session ..."...
			resolved.Session = append([]llm.Message(nil), session...)
			if resolved.Message != nil {
				resolved.Session = append(resolved.Session, *resolved.Message)
			}
			return resolved, nil
		}
```

`pkg/run/llm_middleware.go:220-231` 与 `302-323`、`349-356`：

```go
func (r recoverableLLM) Execute(ctx context.Context, messages []llm.Message, tools []*llm.Tool) (*llm.Result, error) {
	// Each LLM call is a pre-turn or mid-turn auto-compact checkpoint.
	messages, compacted := r.applyRunScopedCompact(ctx, messages, tools)
	if compacted {
		// ...
		recordCompactionAdoption(ctx, messages)
	}
	// ... r.inner.Execute(ctx, messages, tools) ...
	// overflow branch (line ~252):
		if compacted, ok := r.forceCompact(ctx, messages, tools); ok {
			recordCompactionAdoption(ctx, compacted)

func (r recoverableLLM) applyRunScopedCompact(ctx context.Context, messages []llm.Message, tools []*llm.Tool) ([]llm.Message, bool) {
	runID := strings.TrimSpace(tool.RunIDFromContext(ctx))
	prefillTokens := r.runGuard.prefill(runID, llm.EstimateMessages(messages))
	if base, baseLen, ok := r.runGuard.get(runID); ok && baseLen <= len(messages) {
		newMsgs := messages[baseLen:]
		combined := make([]llm.Message, 0, len(base)+len(newMsgs))
		combined = append(combined, base...)
		combined = append(combined, newMsgs...)
		messages = combined
	}
	var snapshotRaw json.RawMessage
	if r.snapshot != nil {
		if raw, ok := r.snapshot(ctx); ok {
			snapshotRaw = raw
		}
	}
	messages, compacted := compactIfNeeded(ctx, messages, snapshotRaw, r.compactDeps, prefillTokens, tools)
	if compacted {
		r.runGuard.set(runID, messages)
	}
	return messages, compacted
}

func (r recoverableLLM) forceCompact(ctx context.Context, messages []llm.Message, tools []*llm.Tool) ([]llm.Message, bool) {
	compacted, ok := r.compactDeps.tryCompact(ctx, messages, tools, true)
	if !ok {
		return messages, false
	}
	r.runGuard.set(strings.TrimSpace(tool.RunIDFromContext(ctx)), compacted)
	return compacted, true
}
```

### web / gateway 的同类问题（2026-10-09 核查）

- **G1 gateway 撤回同样卡在在途窗口（引擎修复覆盖，缺测试）**：主会话 `edit_last`（`pkg/gateway/run_control.go:129-136`）调 `q.Recall()`；subagent `edit_last`（`pkg/gateway/api_extra.go:1891-1896`）调 `run.RecallSubagentInput`。gateway 的 run 把会话队列的 runtime 放进 agent ctx（`pkg/gateway/server.go:1705-1710` 与 `1776`；`pkg/gateway/run_control.go:667-672` 与 `731`），走的是同一个编排循环，所以在途窗口完全相同。Step 4 修好引擎后自动生效，但 gateway 没有测试锁住这一点。
- **G2 web subagent 视图的队列预览会过期**：subagent 的预览事件只在 HTTP 操作之后发（`pkg/gateway/api_extra.go:1834`、`1896`、`1951` → `publishSubagentPendingInputUpdated`，`1755-1772`）。subagent 的队列是 `control.SessionQueue(workerSessionID)`（`pkg/run/subagent.go:661-663`），没有任何 change hook：steer 被 Commit 时引擎只发 `subagent_input_delivered`（`pkg/run/subagent.go:713-735`，web 据此画一条用户块），执行结束时 `OnBoundary` 只发 `queued_input_released`（`pkg/gateway/api_extra.go:1730-1753`），web 的 subagent 分支处理这个事件时不动预览（`frontend/src/composables/useChatStream.ts:2939-2955`）。结果：web 的 subagent 视图里，已送达或已被交回的消息仍挂在队列预览上，按撤回 → `Recall` 失败（第一次按键只是把预览刷新掉）；预览事件是持久化的，刷新页面回放的也是这份旧预览。TUI 每次重绘都读活的通道（`pkg/tui/run.go` 的 `composerPendingInputPreview`），不受影响。
- **G3 web 主会话里送达的 steer 不进实时对话**：只有 TUI 调 `InputQueue.TakeDelivered`（`pkg/tui/run.go` 的 `takeDeliveredSteers`，随后 `renderDeliveredSteerMessages` 画成 you 消息）；gateway 的队列 change hook 只发预览（`pkg/gateway/server.go:1706-1709`、`pkg/gateway/run_control.go:668-671`，两处代码相同），没有任何「steer 已送达」的事件。web 端于是看到 steer 从队列里消失，对话里却什么也没出现；run 结束后它作为 user 行写进 transcript（`pkg/turn/session.go:376-379` 写 `Result.Session`），刷新后的回放在 user 行处把回答拆成两个气泡（`frontend/src/composables/useChatStream.ts:566-580`，`conversationFromTranscript`）。gateway 的会话队列按会话常驻进程（`pkg/run/controller.go` 的 `SessionQueue`），没人取的 `delivered` 列表只增不减。
- **G4 web 的 Shift+← 门控与「队列可见」脱节**：`frontend/src/components/ForebrainPromptTextarea.vue:597` 只在 `props.duringRun` 时把 Shift+←/Alt+↑ 转成 `edit-last-queued`；`ChatView.vue:507` 把 `duringRun` 绑到 `isStreaming`，它只在本页自己发起的 send 流式期间为真。而队列浮层显示的是 `composerPendingInput`（`ChatView.vue:943-946`）：subagent 视图里是该 subagent 自己的队列（主会话空闲时照样有），刷新后进行中 run 的队列也会经事件回放显示出来（回放的 `turn_started` 会设置 `activeRunId`，`useChatStream.ts:3646-3653`）。这两种情况下浮层里的「编辑」按钮能用、Shift+← 却无效。TUI 在空闲时同样处理这个键（`pkg/tui/run.go:4606-4619`）。另外 `editLastQueuedMessage`（`useChatStream.ts:3119-3133`）不捕获接口错误（run 刚结束时 gateway 回 409），而 `recallSubagentInput`（`3222-3234`）会捕获。

web 端的相关约定：
- 前端事件 payload 已经过 camelCase 转换（例：`releasedInputs` 读 `input.mentionImages`，`useChatStream.ts:1352-1363`）。
- run 投影：本页自己的 send 用 `send()` 里的常量 `assistantMessageId` 与 `eventState`（`useChatStream.ts:3986-4003`），并把同一个 `eventState` 对象登记进 `eventProjectionByRun`（`4065`）；其它页面发起的 run 走 `eventProjection(evt)`（`2280` 起）取回同一份 `{assistantMessageId, planBlocks, state}`。所有正文、工具卡事件都经 `handleRunEvent(evt, assistantMessageId, planBlocks, state)`（`3408` 起）落到 `assistantMessageId` 那条消息上；`legacyMirroredEventTypes`（`2354-2361`）只覆盖生命周期事件。
- 刷新时，`transcriptBackedEventTypes`（`2373-2380`）里的历史事件被跳过，因为 transcript 行已经承载它们。

### 修复设计（executor 必须理解的论证）

- **撤回 = 从在途交付里拿掉 + 中止承载它的调用 + 去掉它重发**。runtime 记录未结算的在途交付（`inflight`）；新增 `RetractSteer(seq)` 先查 `pending`、再查在途交付；命中在途交付时从其 entries 删掉、标记 `retracted`、调用编排循环绑定的 `abort`（取消这次调用的 ctx）。被撤回过的交付**拒绝 Commit**；编排循环看到 `Retracted()` 就丢弃这次调用的结果，`Rollback` 把剩余 steer 放回 `pending`，截掉 session 里的 steer 消息，再 `BeginSteerDelivery` 取一次（剩余的 + 期间新入队的）重发。
- **结算互斥**：`SteerDelivery` 的状态改由 `rt.mu` 保护（删掉 `d.mu`），`BeginSteerDelivery` 在同一把锁里「从 pending 取走 + 登记 inflight」，`Rollback` 在同一把锁里「放回 pending + 注销 inflight」——任何时刻 steer 都恰好在 `pending` 或某个未结算交付里，撤回不可能落空。Commit 与撤回谁先拿到锁谁赢：Commit 赢 → 撤回失败（模型已在回答，消息随 hook 立刻离开预览）；撤回赢 → Commit 被拒、调用被丢弃。
- **非流式调用的竞态**：成功返回后先 `Commit()`，再检查 `Retracted()`——两者在锁内互斥，撤回若晚于 Commit 必然失败，不会出现「用户拿回了消息、模型却回答了它」。
- **被丢弃调用的输出不能漏到界面**：`withSteerDeliveryResponseStart` 在 Commit 被拒（已撤回）时不再向下游转发 `OnResponseStarted`，并屏蔽这次调用后续的 `OnDelta`/`OnReasoningDelta`/`OnReasoningDone`/`OnWebSearch`。
- **最终回答处重开的 turn**：在无工具调用的最终回答后取走 steer 时，回答已经追加进 session（`heldAnswer`）。如果这批 steer 全被撤回，turn 应当就以这条回答结束（不能拿「以 assistant 结尾」的 session 再调一次模型）。
- **清空队列**：`Discard` 对每条 steer 按 `Seq` 调 `RetractSteer`（与 `Recall` 同一个入口），撤回成功的计数；runtime detach 时全部计数。撤走在途交付里的全部 steer 后，编排循环走同一条重发路径：没有 steer 剩下就按无 steer 的请求重发（工具边界处），或以原回答结束 turn（最终回答处重开的情况）。
- **web / gateway**：
  - G1 不改代码，只补 gateway 测试锁住「在途 steer 经 `edit_last` 能拿回、承载它的调用不会 Commit」。
  - G2 下沉到引擎：`SubagentSurface` 新增 `OnQueueChanged(agentKey, preview)`；subagent 通道在创建时给自己的队列装 change hook，队列一有变化（入队、撤回、送达、清空、执行结束结算）就把最新预览交给 surface。gateway 的 surface 据此发带 `agent_id` 的 `pending_input_updated`。TUI 的 surface 不需要它（每次重绘都读活的通道）。
  - G3：新增持久事件 `input_delivered`（payload 与 `ReleasedInput` 同形：text、attachments、mention_images）。gateway 把两处重复的 change hook 收敛成 `watchRunQueue`：每次队列变化先 `TakeDelivered()`，逐条发 `input_delivered`，再发预览。delivered 列表因此被取空，不再泄漏。web 收到 `input_delivered` 后收尾当前回答气泡，在它后面插入一条用户消息，再开一个新的回答气泡；这个 run 后续的所有输出都落到新气泡上。为此 `RunProjectionState` 新增 `answerMessageId`，`handleRunEvent` 和 `send()` 的收尾代码都改为先通过 `answerTarget(state, 起始 id)` 解析当前目标。刷新时历史的 `input_delivered` 被跳过（列入 `transcriptBackedEventTypes`），以 transcript 为准。
  - G4：输入框新增 `queueVisible` 属性；只要浮层里有消息，Shift+←/Alt+↑ 就转成撤回，与 run 是否由本页发起无关。`editLastQueuedMessage` 像 `recallSubagentInput` 一样捕获接口错误、返回 null。
- **压缩（owner 裁决）**：编排循环通过 ctx 告诉 `recoverableLLM`「这次请求末尾 N 条消息是暂定的」（在途 steer，以及最终回答处重开时那条回答）。压缩只对前面的历史做 checkpoint，暂定消息原样接在 checkpoint 之后；`runGuard` 缓存的 base 只是 checkpoint 本身。压缩在「保留所有 ctx 值、但只随编排 ctx 取消」的派生 ctx 上跑，所以撤回不会中断压缩、压缩结果照样被采纳。撤回重发时如果 adoption sink 里有压缩结果，它一定以那 N 条暂定消息结尾：保留 checkpoint，去掉 steer 那几条。

### 仓库约定

- 代码注释用英文，解释「为什么」，与行为严格一致；见 `pkg/run/turn_input.go` 现有注释风格（长段落、无 TODO）。改动行为的地方必须同步改注释。
- 测试与生产代码同包（`package run`），测试名用完整句子式的 `TestXxx`，失败信息说明用户可见后果；范本：`pkg/run/orchestration_llm_test.go:4129-4171`（`TestFailedSamplingReturnsDrainedSteerToQueue`）、`pkg/run/compaction_test.go:44-93`。
- 词汇：steer 的撤回在本仓一律叫 **retract**（`RetractLastSteer`、`RetractSurfaceSteer`）；不要用 withdraw（withdraw 在本仓专指 Esc 撤回刚提交的消息）。
- **不 commit**：owner 手动审阅提交。

## Commands you will need

| Purpose | Command | Expected on success |
|---|---|---|
| 相关单测 | `CGO_ENABLED=1 go test -tags fts5 ./pkg/run -run 'Retract|Steer|Recall|InputQueue|Compact|Overflow' -count=1` | `ok` |
| 竞态检测 | `CGO_ENABLED=1 go test -race -tags fts5 ./pkg/run -run 'Retract|Steer|Recall|InputQueue|Compact|Overflow' -count=1` | `ok`，无 `DATA RACE` |
| 包内全量 | `CGO_ENABLED=1 go test -tags fts5 ./pkg/run -count=1` | `ok` |
| 全量（CI 同款） | `CGO_ENABLED=1 go test -tags fts5 ./... -count=1 -timeout 20m` | 全部 `ok` |
| Vet | `go vet ./pkg/run/...` | 无输出 |
| gofmt（CI gate） | `gofmt -l cmd pkg third_party` | 无输出 |
| 构建真机二进制 | `.claude/skills/run-forebrain/driver.sh build` | exit 0 |
| gateway 相关 | `CGO_ENABLED=1 go test -tags fts5 ./pkg/gateway -run 'TestHandleRunQueuedInput|TestHandleRunInput|TestRunQueue|TestSubagentSurface|TestSubagentQueuedInput|TestPendingInput' -count=1` | `ok` |
| subagent 竞态 | `CGO_ENABLED=1 go test -race -tags fts5 ./pkg/run -run 'Subagent' -count=1` | `ok`，无 `DATA RACE` |
| 前端相关 | `pnpm -C frontend exec vitest run src/components/ForebrainPromptTextarea.test.ts src/composables/useChatStream.test.ts src/views/ChatView.test.ts` | 全部 passed |
| 前端全量 | `pnpm -C frontend test` | 全部 passed |

基线（commit `71c0449` 已实测）：第一行命令全部 PASS，耗时约 7 秒；gateway 相关命令 `ok`；前端相关命令 2 个文件 104 个用例全过；前端全量 40 个文件 315 个用例全过。

**不要运行 `vue-tsc` 或 `vite build`**：`frontend/tsconfig.tsbuildinfo`、`frontend/tsconfig.node.tsbuildinfo` 被 git 跟踪，类型检查/构建会改写它们，污染工作区。

## Suggested executor toolkit

- `run-forebrain` skill（`.claude/skills/run-forebrain/SKILL.md`）：Step 1 与 Step 12 用它在 tmux 里驱动真实 TUI。先读一遍 SKILL.md 的 Run 与 Gotchas 段。需要 `tmux`（`brew install tmux`）。

## Scope

**In scope**（只允许改这些文件）：

- `pkg/run/turn_input.go`
- `pkg/run/turn_input_test.go`
- `pkg/run/orchestration_llm.go`
- `pkg/run/orchestration_llm_test.go`
- `pkg/run/compaction.go`
- `pkg/run/compaction_test.go`
- `pkg/run/llm_middleware.go`
- `.claude/skills/run-forebrain/fake_provider.py`（加 `FAKE_REPLY_DELAY`）
- `.claude/skills/run-forebrain/SKILL.md`（一句话记录 `FAKE_REPLY_DELAY`）
- `pkg/run/subagent.go`、`pkg/run/subagent_test.go`（G2：`SubagentSurface.OnQueueChanged` 与通道的队列 hook）
- `pkg/event/run_events.go`（G3：`RunEventInputDelivered` 与 `InputDeliveredPayload`）
- `pkg/gateway/run_control.go`、`pkg/gateway/server.go`、`pkg/gateway/api_extra.go`（G2/G3：`watchRunQueue`、subagent surface 推送预览）
- `pkg/gateway/run_control_test.go`、`pkg/gateway/api_extra_test.go`
- `frontend/src/composables/useChatStream.ts`、`frontend/src/composables/useChatStream.test.ts`（G3/G4）
- `frontend/src/components/ForebrainPromptTextarea.vue`、`frontend/src/components/ForebrainPromptTextarea.test.ts`、`frontend/src/views/ChatView.vue`（G4）
- `frontend/src/lib/forebrainGatewayRuntime.ts`（G3：事件类型联合里加 `input_delivered`）

**Out of scope**（看起来相关，也不要动）：

- `pkg/tui/**` — TUI 是标准语义，已经正确：重绘读活队列、送达的 steer 用 `TakeDelivered` 画成 you 消息、空闲时也处理撤回键。TUI 的 `subagentSurface` 不需要 `OnQueueChanged`。
- `frontend/src/components/PendingInputQueuePopover.vue` — 浮层的「编辑」按钮已经与 run 归属无关。
- `frontend/tsconfig*.tsbuildinfo`、`frontend/dist` — 不要产生任何改动。
- `InputQueue.TakeAll`、`InputQueue.Next`、`consumeSteersLocked` — 调用时机决定了不会遇到在途交付（见「清空队列的同类缺口」末条），不改。
- `RetractLastSteer` — 本计划之后不再有生产调用方，但保留原样（连同其测试），不要删除。
- `pkg/assembly/**`（压缩服务本身）、`compactIfNeeded`/`shouldCompactMessages` 的阈值逻辑 — 本计划只改「给压缩什么输入、在什么 ctx 上跑」。
- 任何 reminder 包装（`wrapPlanModeLLM`、`wrapSkillOfferLLM`、`wrapLSPDiagnosticsReminderLLM`）。

## Git workflow

- **不建分支、不 commit、不 push、不开 PR**：改动直接留在当前工作区，由 owner 手动审阅提交（本仓既定工作流）。
- 如 owner 需要 commit message 建议：`fix(run,gateway,web): recall a queued steer until the model starts answering it`（或按 engine / gateway / web 拆成三个提交）。

## Steps

### Step 1: 给假 provider 加首 token 延迟，并在改代码前复现 bug

**1a. 改 `.claude/skills/run-forebrain/fake_provider.py`**：

在 `DRIP_DELAY = ...`（第 90 行）之后加：

```python
# FAKE_REPLY_DELAY holds every plain-text reply of a scripted mode for that many
# seconds before its first byte. A queued steer rides on the request that
# follows a scripted tool call, so this is what keeps the window between the
# steer leaving the queue and the model's first output open long enough to act
# in it.
REPLY_DELAY = float(os.environ.get("FAKE_REPLY_DELAY") or 0)
```

在 `serve_tool_script`（约 508 行）的 docstring 之后、`self.send_response(200)` 之前插入：

```python
        if nth >= len(TOOL_SCRIPT) and REPLY_DELAY > 0:
            # No bytes at all until the delay is over, like a provider still
            # working on its first token.
            time.sleep(REPLY_DELAY)
```

（服务器是 `ThreadingTCPServer`，一个请求睡着不会挡住下一个请求。被 forebrain 中止的那个请求睡醒后往已关闭的连接写数据，`provider.log` 里会出现一段 `BrokenPipeError` 回溯——这是预期现象，不是错误。）

在模块 docstring 的 `tool` 模式说明末尾补一句：`FAKE_REPLY_DELAY=<seconds> holds the plain-text replies that follow the script, which is how to keep a queued steer in flight.`

在 `.claude/skills/run-forebrain/SKILL.md` 中 `COMPACT_LIMIT=300 $D start drip ...` 那一段之后加一段：

```markdown
`FAKE_REPLY_DELAY=20 $D start tool '{"name":"shell","arguments":{"command":"sleep 6"}}'`
holds the reply that follows the scripted tool call for 20 seconds: a message
queued while the tool runs is taken for that request and sits in flight —
still shown in the queue, not yet answered — for the whole delay.
```

**Verify**: `python3 -c "import ast,sys; ast.parse(open('.claude/skills/run-forebrain/fake_provider.py').read())"` → exit 0。

**1b. 用改动前的代码复现**（证明验收脚本能打到这个窗口）。把下面脚本存为 scratch 文件执行（不要放进仓库）：

```bash
set -u
D=.claude/skills/run-forebrain/driver.sh
DUMP="$(mktemp -d)"
$D build
FAKE_REPLY_DELAY=20 FAKE_DUMP_DIR="$DUMP" $D start tool '{"name":"shell","arguments":{"command":"sleep 6"}}'
$D submit 'run the slow command'
$D wait 'esc to interrupt' 25
$D submit 'RECALL-ME-STEER'
# wait until a request carrying the steer has gone out (the shell finished and the loop took the steer)
for i in $(seq 1 60); do grep -l 'RECALL-ME-STEER' "$DUMP"/request-*.json >/dev/null 2>&1 && break; sleep 0.5; done
grep -l 'RECALL-ME-STEER' "$DUMP"/request-*.json || { echo "STEER NEVER WENT OUT"; exit 1; }
before=$(ls "$DUMP" | wc -l)
$D screen > "$DUMP/screen-before.txt"
$D send "$(printf '\033[1;2D')"      # Shift+Left
sleep 2
$D screen > "$DUMP/screen-after.txt"
after=$(ls "$DUMP" | wc -l)
echo "requests before=$before after=$after"
cat "$DUMP/screen-after.txt"
$D stop
echo "dump dir: $DUMP"
```

**Expected（修复前）**：`requests before=N after=N`（按键后没有新请求）；`screen-after.txt` 里 composer 是空的，`RECALL-ME-STEER` 仍只出现在队列预览里（与 `screen-before.txt` 相同）。记下这两份 screen 的关键几行，最后报告里要作为「修复前」证据。

### Step 2: runtime 登记在途交付，新增 `RetractSteer`

**文件**: `pkg/run/turn_input.go`。本步只加能力，不改 `Recall`，行为不变。

**2a.** `TurnInputRuntime`（29-33 行）加字段：

```go
type TurnInputRuntime struct {
	mu      sync.Mutex
	pending []TurnInputEntry
	// inflight are the deliveries taken for a model call that have not settled.
	// A steer in one of them has left pending but is still the user's to take
	// back until its call commits (see RetractSteer).
	inflight []*SteerDelivery
	onChange []func(delivered []TurnInputEntry)
}
```

**2b.** 把 `takeSteers`（65-91 行）拆成加锁外壳 + `takeSteersLocked`（函数体逻辑不变，只是去掉 Lock/Unlock，调用方持锁）：

```go
// takeSteers removes the queued steers without reporting them as delivered.
func (r *TurnInputRuntime) takeSteers(ctx context.Context) []TurnInputEntry {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.takeSteersLocked(ctx)
}

func (r *TurnInputRuntime) takeSteersLocked(ctx context.Context) []TurnInputEntry {
	if len(r.pending) == 0 || (ctx != nil && ctx.Err() != nil) {
		return nil
	}
	// ...原 75-88 行循环原样搬过来（out/rest 构造、r.pending = rest）...
	return out
}
```

**2c.** 删除 `restoreSteers`（107-120 行）——它唯一的调用方是 `Rollback`，其逻辑（含那段 FIFO 注释）搬进新的 `Rollback`（见 2e）。先确认：`grep -rn "restoreSteers" pkg` 只命中 `pkg/run/turn_input.go`。

**2d.** 替换 `SteerDelivery` 的 doc 注释第一段与 struct（122-140 行）。第二段「Delivery is not settled by the drain itself...」原样保留：

```go
// SteerDelivery is one in-flight handoff of queued steers to the model. The
// entries have left the runtime's pending list, but they are not reported as
// delivered until Commit — and until then the user can still take one back
// (see TurnInputRuntime.RetractSteer): the call carrying it is abandoned and
// the orchestration loop sends what is left without it.
//
// <keep the existing "Delivery is not settled by the drain itself. ..." paragraph verbatim>
//
// A delivery's state lives under its runtime's lock, so a steer is always in
// exactly one place — pending, or an unsettled delivery — and a retraction
// can never slip between the two.
type SteerDelivery struct {
	rt        *TurnInputRuntime
	entries   []TurnInputEntry
	settled   bool
	retracted bool   // the user took a steer back out before the call settled
	abort     func() // abandons the model call carrying the delivery
}
```

**2e.** 替换 `BeginSteerDelivery`（函数体）、`Entries`、`Commit`、`Rollback`（142-208 行）。`BeginSteerDelivery` 的 doc 注释原样保留：

```go
func (r *TurnInputRuntime) BeginSteerDelivery(ctx context.Context) *SteerDelivery {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	entries := r.takeSteersLocked(ctx)
	if len(entries) == 0 {
		return nil
	}
	d := &SteerDelivery{rt: r, entries: entries}
	r.inflight = append(r.inflight, d)
	return d
}

// Entries returns the steers this delivery is still carrying: a copy, since a
// retraction can shrink the delivery while its call is in flight.
func (d *SteerDelivery) Entries() []TurnInputEntry {
	if d == nil {
		return nil
	}
	d.rt.mu.Lock()
	defer d.rt.mu.Unlock()
	return append([]TurnInputEntry(nil), d.entries...)
}

// Commit reports the entries as delivered. It is called before the first model
// output event is forwarded, or after a successful non-streaming
// call. It returns true only for the transition that committed the delivery.
// A delivery a steer was retracted from cannot commit: the call carrying it is
// being abandoned whatever it returns, and the loop re-sends the rest.
func (d *SteerDelivery) Commit() bool {
	if d == nil {
		return false
	}
	r := d.rt
	r.mu.Lock()
	if d.settled || d.retracted {
		r.mu.Unlock()
		return false
	}
	d.settled = true
	r.forgetDeliveryLocked(d)
	entries := d.entries
	r.mu.Unlock()
	r.notifyDelivered(entries)
	return true
}

// Rollback returns the entries to the queue because the call that was to carry
// them failed before establishing delivery, or was abandoned because the user
// retracted one of them. No change hook fires: the surface was never told they
// left the queue, so its mirror still holds them and the turn boundary — or the
// loop's re-send — takes them from there. It returns true only when this call
// won the settlement, even when a retraction left nothing to restore.
//
// Steers are drained FIFO, so entries that came out first must go back in
// first for the next drain to hand the model the same order the user typed.
func (d *SteerDelivery) Rollback() bool {
	if d == nil {
		return false
	}
	r := d.rt
	r.mu.Lock()
	defer r.mu.Unlock()
	if d.settled {
		return false
	}
	d.settled = true
	r.forgetDeliveryLocked(d)
	if len(d.entries) > 0 {
		restored := make([]TurnInputEntry, 0, len(d.entries)+len(r.pending))
		restored = append(restored, d.entries...)
		restored = append(restored, r.pending...)
		r.pending = restored
	}
	return true
}

// Retracted reports whether the user took a steer back out of this delivery
// before it settled. The call that carried it must not count as delivering
// anything: whatever it produced was produced with the retracted message in
// view, so it is discarded.
func (d *SteerDelivery) Retracted() bool {
	if d == nil {
		return false
	}
	d.rt.mu.Lock()
	defer d.rt.mu.Unlock()
	return d.retracted && !d.settled
}

// bindAbort registers how to abandon the model call carrying this delivery.
// A retraction that landed before the call began aborts it at once.
func (d *SteerDelivery) bindAbort(abort func()) {
	if d == nil || abort == nil {
		return
	}
	d.rt.mu.Lock()
	d.abort = abort
	now := d.retracted && !d.settled
	d.rt.mu.Unlock()
	if now {
		abort()
	}
}

func (r *TurnInputRuntime) forgetDeliveryLocked(d *SteerDelivery) {
	for i, in := range r.inflight {
		if in == d {
			r.inflight = append(r.inflight[:i], r.inflight[i+1:]...)
			return
		}
	}
}
```

**2f.** 在 `RetractLastSteer`（210-230 行）之后新增：

```go
// RetractSteer takes back the steer the conversation queue stamped with seq,
// so the model never answers it. A steer still pending just leaves the list.
// One already taken for a model call that has not begun answering is
// retracted from that delivery: the call is aborted, and the orchestration
// loop re-sends what the delivery still carries without it. It returns false
// only when the steer is out of reach — committed, so the model is answering
// it — or was never queued here.
func (r *TurnInputRuntime) RetractSteer(seq int) bool {
	if r == nil || seq <= 0 {
		return false
	}
	r.mu.Lock()
	for i, entry := range r.pending {
		if entry.Mode == TurnInputModeSteer && entry.Seq == seq {
			r.pending = append(r.pending[:i], r.pending[i+1:]...)
			r.mu.Unlock()
			return true
		}
	}
	for _, d := range r.inflight {
		for i, entry := range d.entries {
			if entry.Seq != seq {
				continue
			}
			d.entries = append(d.entries[:i], d.entries[i+1:]...)
			d.retracted = true
			abort := d.abort
			r.mu.Unlock()
			if abort != nil {
				abort()
			}
			return true
		}
	}
	r.mu.Unlock()
	return false
}
```

（`seq <= 0` 的守卫：直接 `rt.Enqueue` 进来的条目 Seq 为 0，只有 `InputQueue` 从 1 开始盖时钟。）

**2g.** 在 `pkg/run/turn_input_test.go` 的 `TestRecallSteerAfterRuntimeDetach` 之后追加三个测试：

```go
// A steer taken for a model call that has not committed is still the user's:
// retracting it shrinks the delivery, aborts the call carrying it, and keeps
// that call from committing; rolling back returns only what is left.
func TestRetractSteerTakesItBackFromAnInFlightDelivery(t *testing.T) {
	q := NewInputQueue()
	rt := NewTurnInputRuntime()
	q.Attach(rt)
	q.Steer(Input{Text: "first"})
	q.Steer(Input{Text: "second"})
	delivery := rt.BeginSteerDelivery(context.Background())
	if delivery == nil || len(delivery.Entries()) != 2 {
		t.Fatalf("expected both steers in flight, got %#v", delivery.Entries())
	}
	aborted := 0
	delivery.bindAbort(func() { aborted++ })
	if !rt.RetractSteer(2) {
		t.Fatal("a steer whose call has not begun answering must be retractable")
	}
	if aborted != 1 {
		t.Fatalf("aborts=%d want 1: the call carrying the steer must be abandoned", aborted)
	}
	if got := delivery.Entries(); len(got) != 1 || got[0].Seq != 1 {
		t.Fatalf("delivery still carries %#v, want only the first steer", got)
	}
	if !delivery.Retracted() {
		t.Fatal("the delivery must report the retraction")
	}
	if delivery.Commit() {
		t.Fatal("a delivery a steer was retracted from must not commit")
	}
	if rt.RetractSteer(2) {
		t.Fatal("the same steer was retracted twice")
	}
	if !delivery.Rollback() {
		t.Fatal("rollback must settle the abandoned delivery")
	}
	if got := rt.Snapshot(); len(got) != 1 || got[0].Seq != 1 {
		t.Fatalf("rollback restored %#v, want only the steer that is left", got)
	}
	if delivery.Retracted() {
		t.Fatal("a settled delivery is no longer in flight")
	}
}

// A retraction can land between the tool boundary that took the steer and the
// start of the call that will carry it; binding the call's abort then fires it
// at once, so that call is never made with the steer.
func TestRetractSteerBeforeTheCallStartsAbortsItOnBind(t *testing.T) {
	q := NewInputQueue()
	rt := NewTurnInputRuntime()
	q.Attach(rt)
	q.Steer(Input{Text: "only"})
	delivery := rt.BeginSteerDelivery(context.Background())
	if delivery == nil {
		t.Fatal("expected a delivery")
	}
	if !rt.RetractSteer(1) {
		t.Fatal("expected the in-flight steer to be retractable")
	}
	aborted := 0
	delivery.bindAbort(func() { aborted++ })
	if aborted != 1 {
		t.Fatalf("aborts=%d want 1", aborted)
	}
}

// Once the model is answering a steer it is out of reach.
func TestRetractSteerFailsOnceCommitted(t *testing.T) {
	q := NewInputQueue()
	rt := NewTurnInputRuntime()
	q.Attach(rt)
	q.Steer(Input{Text: "answered"})
	delivery := rt.BeginSteerDelivery(context.Background())
	if delivery == nil || !delivery.Commit() {
		t.Fatal("expected a committed delivery")
	}
	if rt.RetractSteer(1) {
		t.Fatal("a committed steer was retracted")
	}
	if rt.RetractSteer(0) {
		t.Fatal("seq 0 never names a queued steer")
	}
}
```

**Verify**:
- `CGO_ENABLED=1 go test -tags fts5 ./pkg/run -run 'Retract|Steer|Recall|InputQueue' -count=1` → `ok`（包括现有的 `"unretractable steer is skipped"`——本步还没改 `Recall`）。
- `CGO_ENABLED=1 go test -race -tags fts5 ./pkg/run -run 'Retract|InputQueue' -count=1` → `ok`。

### Step 3: 压缩不吞暂定消息，且跑在不受撤回影响的 ctx 上

**3a.** 在 `pkg/run/compaction.go` 中 `recordCompactionAdoption`（248-256 行）之后新增：

```go
// provisionalTail marks the trailing messages of one model call that are not
// settled history yet: the queued steers riding on it — and, when they reopened
// a finished turn, the answer they follow — any of which the user can still
// retract until the call commits. recoverableLLM keeps them out of a
// checkpoint, which is durable: a retracted steer must never survive in it, and
// a steer rolled back after a failed call is sent again, so folding it in would
// send it twice.
type provisionalTail struct {
	sent  int             // length of the message slice the call was handed
	count int             // how many of its trailing messages are provisional
	outer context.Context // the orchestration context, without the call's retraction abort
}

type provisionalTailKey struct{}

// withProvisionalTail marks the last count of the sent messages handed to the
// call made under ctx as provisional. outer is the context the call's own was
// derived from: a compaction runs on its cancellation, not the call's, so a
// retraction that aborts the call does not throw away a checkpoint that never
// included the retracted steer.
func withProvisionalTail(ctx, outer context.Context, sent, count int) context.Context {
	if count <= 0 || count > sent {
		return ctx
	}
	return context.WithValue(ctx, provisionalTailKey{}, provisionalTail{sent: sent, count: count, outer: outer})
}

// provisionalTailLen is how many trailing messages of a sent slice are
// provisional — zero unless ctx describes exactly this slice, so a nested call
// that inherits the context with other messages compacts them whole, as before.
func provisionalTailLen(ctx context.Context, sent int) int {
	if ctx == nil {
		return 0
	}
	pt, ok := ctx.Value(provisionalTailKey{}).(provisionalTail)
	if !ok || pt.sent != sent {
		return 0
	}
	return pt.count
}

// compactionContext is ctx minus the call's retraction abort: it keeps every
// value, but it is cancelled only when the orchestration context is. Without a
// provisional tail there is nothing to retract, and ctx is returned as is.
func compactionContext(ctx context.Context) (context.Context, func()) {
	pt, ok := ctx.Value(provisionalTailKey{}).(provisionalTail)
	if !ok || pt.outer == nil {
		return ctx, func() {}
	}
	detached, cancel := context.WithCancelCause(context.WithoutCancel(ctx))
	stop := context.AfterFunc(pt.outer, func() { cancel(context.Cause(pt.outer)) })
	return detached, func() {
		stop()
		cancel(nil)
	}
}

// joinProvisionalTail appends the provisional messages after a checkpoint, in a
// fresh slice so the checkpoint the run guard caches is never written through.
func joinProvisionalTail(head, tail []llm.Message) []llm.Message {
	if len(tail) == 0 {
		return head
	}
	out := make([]llm.Message, 0, len(head)+len(tail))
	out = append(out, head...)
	return append(out, tail...)
}
```

（`compaction.go` 已 import `context` 与 `llm`，无需新增 import。）

**3b.** 改 `pkg/run/llm_middleware.go` 的 `applyRunScopedCompact`（298-323 行，连 doc 注释整体替换）：

```go
// applyRunScopedCompact keeps a compacted checkpoint as the base for later LLM
// calls in the same run, then appends messages produced after that checkpoint.
// It reports whether a new compaction happened on this call so the caller can
// propagate the replacement history to the orchestration loop, and how many
// trailing messages are provisional (see provisionalTail): those stay out of
// the checkpoint and follow it verbatim.
func (r recoverableLLM) applyRunScopedCompact(ctx context.Context, messages []llm.Message, tools []*llm.Tool) ([]llm.Message, int, bool) {
	runID := strings.TrimSpace(tool.RunIDFromContext(ctx))
	prefillTokens := r.runGuard.prefill(runID, llm.EstimateMessages(messages))
	tail := provisionalTailLen(ctx, len(messages))
	if base, baseLen, ok := r.runGuard.get(runID); ok && baseLen <= len(messages) {
		newMsgs := messages[baseLen:]
		if tail > len(newMsgs) {
			// A cached base reaching into the provisional messages is not the
			// history the orchestration loop described; compact the slice whole,
			// as before.
			tail = 0
		}
		combined := make([]llm.Message, 0, len(base)+len(newMsgs))
		combined = append(combined, base...)
		combined = append(combined, newMsgs...)
		messages = combined
	}
	head, provisional := messages[:len(messages)-tail], messages[len(messages)-tail:]
	var snapshotRaw json.RawMessage
	if r.snapshot != nil {
		if raw, ok := r.snapshot(ctx); ok {
			snapshotRaw = raw
		}
	}
	compactCtx, release := compactionContext(ctx)
	head, compacted := compactIfNeeded(compactCtx, head, snapshotRaw, r.compactDeps, prefillTokens, tools)
	release()
	if !compacted {
		return messages, tail, false
	}
	r.runGuard.set(runID, head)
	return joinProvisionalTail(head, provisional), tail, true
}
```

**3c.** 替换 `forceCompact`（349-356 行）：

```go
func (r recoverableLLM) forceCompact(ctx context.Context, messages []llm.Message, tail int, tools []*llm.Tool) ([]llm.Message, bool) {
	head, provisional := messages[:len(messages)-tail], messages[len(messages)-tail:]
	compactCtx, release := compactionContext(ctx)
	compacted, ok := r.compactDeps.tryCompact(compactCtx, head, tools, true)
	release()
	if !ok {
		return messages, false
	}
	r.runGuard.set(strings.TrimSpace(tool.RunIDFromContext(ctx)), compacted)
	return joinProvisionalTail(compacted, provisional), true
}
```

**3d.** 改 `recoverableLLM.Execute`（220 行起）的两处调用：

```go
	messages, tail, compacted := r.applyRunScopedCompact(ctx, messages, tools)
```

以及溢出分支（约 252 行）：

```go
		if compacted, ok := r.forceCompact(ctx, messages, tail, tools); ok {
```

（`tail` 在压缩后依然有效：join 把暂定消息原样留在末尾。）

**3e.** 在 `pkg/run/orchestration_llm_test.go` 的 `TestStreamingOverflowRecoversAndContinues` 之后追加（该文件 `tool` 包别名是 `toolpkg`）：

```go
// The reactive compaction a provider overflow forces must keep the steers
// riding on the call out of its checkpoint, exactly like the proactive one.
func TestOverflowCompactionKeepsProvisionalSteersOutOfTheCheckpoint(t *testing.T) {
	llm.ResetObservedForTest()
	history := append(overflowTestHistory(40), llm.UserMessage(llm.Text("steer riding on the call")))
	inner := &streamOverflowLLM{limit: llm.EstimateMessages(history) / 3}
	var compactedInput []llm.Message
	deps := &CompactChainDeps{
		ActiveModel: func(context.Context) (string, string) { return "openai", "gpt-5.1-codex" },
		TryCompact: func(_ context.Context, msgs []llm.Message, _ []*llm.Tool, _ bool) ([]llm.Message, bool, error) {
			compactedInput = append([]llm.Message(nil), msgs...)
			return []llm.Message{
				llm.SystemMessage("system prompt"),
				llm.UserMessage(llm.Text("summary of prior work")),
			}, true, nil
		},
	}
	adoption := &compactionAdoptionSink{}
	ctx := withCompactionAdoptionSink(toolpkg.WithRunID(context.Background(), "run-overflow-steer"), adoption)
	ctx = withProvisionalTail(ctx, ctx, len(history), 1)
	if _, err := WrapRecoverableLLM(inner, nil, deps).Execute(ctx, history, nil); err != nil {
		t.Fatalf("overflow was not recovered: %v", err)
	}
	if len(compactedInput) != len(history)-1 {
		t.Fatalf("compaction saw %d messages, want the %d before the steer", len(compactedInput), len(history)-1)
	}
	replaced, ok := adoption.take()
	if !ok || replaced[len(replaced)-1].TextContent() != "steer riding on the call" {
		t.Fatalf("the request after the checkpoint must still end with the steer, got %v", roles(replaced))
	}
}
```

**Verify**:
- `CGO_ENABLED=1 go test -tags fts5 ./pkg/run -run 'Compact|Overflow' -count=1` → `ok`（含新测试与 `TestStreamingOverflowRecoversAndContinues`、`TestOrchestrationAutoCompactsDuringTurnWithoutSnapshot`、`TestOrchestrationAdoptsCompactedSession`）。
- `go vet ./pkg/run/...` → 无输出。

### Step 4: 编排循环支持撤回中止重发，`Recall` 改用 `RetractSteer`

**4a. `pkg/run/orchestration_llm.go`**：

1. import 块加 `"sync/atomic"`。
2. 在 `withSteerDeliveryResponseStart` 之前新增：

```go
// errSteerRetracted is the cause a model call is abandoned with when the user
// takes back a queued steer it was carrying before the model began answering.
var errSteerRetracted = errors.New("queued steer retracted before the model answered it")
```

3. 把 164-201 行（`inFlightSteers` 声明到 `inFlightSteers = nil`）替换为下面这段。注意：原 172-175 行的 snapshot 注释、原 180-187 行的失败回滚注释、原 198-200 行的非流式 commit 注释要保留（下面用 `<keep ...>` 标出位置）；原 266-277 行那段「Propagate the full orchestration session ...」长注释搬进 `endTurn`：

```go
	// inFlightSteers carries the queued steers appended to the session for the
	// next model call, held provisionally until that call begins producing output:
	// sessionBeforeSteers is where they start, so a failed call can unwind them
	// out of the session as well as back into the queue.
	var inFlightSteers *SteerDelivery
	sessionBeforeSteers := 0
	// heldAnswer is the tool-call-free answer a steer delivery reopened the turn
	// after; it sits in the session right before those steers. If the user
	// retracts every one of them before the model answers, the turn ends on it,
	// exactly as it would have with no steer queued.
	var heldAnswer *llm.Result
	// endTurn closes the turn on a tool-call-free answer.
	endTurn := func(res *llm.Result, latestUsage *llm.Usage) (*llm.Result, error) {
		resolved, stopErr := w.applyStopHooks(ctx, session, res)
		if stopErr != nil {
			return nil, stopErr
		}
		if !sameUsage(latestUsage, resolved) {
			accumulateUsage(&totalUsage, usageCopy(resolved))
		}
		attachTotalUsage(&totalUsage, resolved)
		// <keep the long "Propagate the full orchestration session ..." comment here>
		resolved.Session = append([]llm.Message(nil), session...)
		if resolved.Message != nil {
			resolved.Session = append(resolved.Session, *resolved.Message)
		}
		return resolved, nil
	}
	for {
		ctx = w.state.ContextWithRuntimeSessionMode(ctx)
		// <keep the "Snapshot the session right before the LLM call" comment>
		partialCapture.Set(session)
		// The call runs under its own cancellation, so a retracted steer can
		// abandon it without cancelling the run.
		callCtx, abandonCall := context.WithCancelCause(ctx)
		if inFlightSteers != nil {
			provisionalFrom := sessionBeforeSteers
			if heldAnswer != nil {
				provisionalFrom--
			}
			callCtx = withProvisionalTail(callCtx, ctx, len(session), len(session)-provisionalFrom)
			inFlightSteers.bindAbort(func() { abandonCall(errSteerRetracted) })
		}
		callCtx = withSteerDeliveryResponseStart(callCtx, inFlightSteers)
		res, err := w.inner.Execute(callCtx, session, tools)
		abandonCall(nil)
		if err == nil {
			// <keep the "A successful non-streaming call has no response-start event, so commit here. ..." comment>
			// Committing before the retraction check below is what makes the two
			// exclusive: a retraction that lands after this commit finds the
			// steers delivered and fails.
			inFlightSteers.Commit()
		}
		if inFlightSteers.Retracted() {
			if ctx.Err() == nil {
				// The user took a steer back out of this call before the model
				// began answering it. Whatever the call returned — the abort's
				// cancellation, or an answer written with the retracted message in
				// view — is discarded; only the tokens it spent are counted. The
				// steers still riding on it go back to the queue and are taken
				// again for a fresh call, together with any queued since.
				accumulateUsage(&totalUsage, usageCopy(res))
				steerCount := len(session) - sessionBeforeSteers
				inFlightSteers.Rollback()
				inFlightSteers = nil
				session = session[:sessionBeforeSteers]
				if replaced, ok := adoptionSink.take(); ok && len(replaced) >= steerCount {
					// A mid-turn compaction finished inside the abandoned call. Its
					// checkpoint stops before the steers (see provisionalTail), so
					// the replacement ends with exactly the steer messages: keep the
					// checkpoint, drop them.
					session = replaced[:len(replaced)-steerCount]
				}
				// Reminders injected into the abandoned request were never part of
				// an exchange; the next call injects them again.
				reminderSink.discard()
				if delivery := TurnInputRuntimeFromContext(ctx).BeginSteerDelivery(ctx); delivery != nil {
					inFlightSteers = delivery
					sessionBeforeSteers = len(session)
					for _, entry := range delivery.Entries() {
						session = append(session, llm.UserMessage(entry.Parts...))
					}
					continue
				}
				if heldAnswer != nil {
					// Every steer that reopened the turn is gone: end it on the
					// answer it had already reached, whose usage was counted when
					// it arrived.
					answer := heldAnswer
					heldAnswer = nil
					session = session[:len(session)-1]
					partialCapture.Set(session)
					return endTurn(answer, usageCopy(answer))
				}
				continue
			}
			// The run itself is being cancelled too: settle this call the way
			// any failed call is settled below.
			if err == nil {
				res, err = nil, ctx.Err()
			}
		}
		if err != nil {
			// <keep the existing failure comment>
			if inFlightSteers != nil {
				rolledBack := inFlightSteers.Rollback()
				inFlightSteers = nil
				if rolledBack {
					session = session[:sessionBeforeSteers]
					partialCapture.Set(session)
				}
			}
			return res, err
		}
		inFlightSteers = nil
		heldAnswer = nil
```

（原 201 行 `inFlightSteers.Commit()` 已上移；原 202 行 `inFlightSteers = nil` 由上面最后两行取代。之后的 adoption / reminder / usage 代码原样不动。）

4. 无工具调用分支（原 245-282 行）改为：

```go
			if res != nil && res.Message != nil {
				if delivery := TurnInputRuntimeFromContext(ctx).BeginSteerDelivery(ctx); delivery != nil {
					session = append(session, *res.Message)
					noteResponseCompleted()
					heldAnswer = res
					sessionBeforeSteers = len(session)
					for _, entry := range delivery.Entries() {
						session = append(session, llm.UserMessage(entry.Parts...))
					}
					inFlightSteers = delivery
					partialCapture.Set(session)
					continue
				}
			}
			return endTurn(res, latestUsage)
		}
```

（原 258-282 行的 stop hook / usage / `resolved.Session` 代码已搬进 `endTurn`，此处删除；该分支上方的 steer 注释保留。工具边界的取走代码 317-335 行不动。）

5. 替换 `withSteerDeliveryResponseStart`（339-363 行），doc 注释在原文末尾追加一句：

```go
// withSteerDeliveryResponseStart binds a provisional delivery to the provider's
// response boundary. <keep the existing text verbatim>
//
// If the user retracted a steer from the delivery before that boundary, the
// call is being abandoned: the commit is refused, and nothing the response
// produces — the acknowledgement included — reaches the surface.
func withSteerDeliveryResponseStart(ctx context.Context, delivery *SteerDelivery) context.Context {
	if delivery == nil {
		return ctx
	}
	sink := llm.StreamSinkFrom(ctx)
	if sink == nil {
		return ctx
	}
	wrapped := *sink
	var abandoned atomic.Bool
	downstream := wrapped.OnResponseStarted
	wrapped.OnResponseStarted = func() {
		delivery.Commit()
		if delivery.Retracted() {
			abandoned.Store(true)
			return
		}
		if downstream != nil {
			downstream()
		}
	}
	if onDelta := wrapped.OnDelta; onDelta != nil {
		wrapped.OnDelta = func(text string) {
			if !abandoned.Load() {
				onDelta(text)
			}
		}
	}
	if onReasoning := wrapped.OnReasoningDelta; onReasoning != nil {
		wrapped.OnReasoningDelta = func(text string) {
			if !abandoned.Load() {
				onReasoning(text)
			}
		}
	}
	if onReasoningDone := wrapped.OnReasoningDone; onReasoningDone != nil {
		wrapped.OnReasoningDone = func() {
			if !abandoned.Load() {
				onReasoningDone()
			}
		}
	}
	if onWebSearch := wrapped.OnWebSearch; onWebSearch != nil {
		wrapped.OnWebSearch = func(id, detail string, completed bool) {
			if !abandoned.Load() {
				onWebSearch(id, detail, completed)
			}
		}
	}
	return llm.WithStreamSink(ctx, &wrapped)
}
```

**4b. `pkg/run/turn_input.go` 的 `Recall`**：

doc 注释（604-615 行）替换为：

```go
// Recall pulls the most recently queued message back out for editing.
// "Most recently queued" is decided by the shared enqueue clock, not by lane
// precedence: the lanes are separate FIFOs, so picking one of them first
// would recall an older message whenever the newest happened to land in
// another lane. While a runtime is attached a steer also lives there, so it is
// retracted from it first — from the pending list, or from the model call that
// is carrying it but has not begun answering, which is then abandoned and
// re-sent without it. A steer shows in the queue until that call commits, and
// it stays recallable for exactly as long. A retraction fails only once the
// model is answering the steer — and since the runtime delivers FIFO, every
// older steer is being answered too — so the whole steer lane is skipped
// rather than handing back an editable copy of a message that is being
// answered. A detached runtime leaves the steer only in the queue — delivery
// settles inside the run loop before the run can end, so it was never
// delivered — and the steer can be recalled directly.
```

`case laneSteer:`（630-643 行）替换为：

```go
		case laneSteer:
			// While a runtime is attached the steer also lives there — pending,
			// or carried by a model call that has not begun answering — and must
			// be retracted from it too. When the runtime is detached, the steer
			// never left the queue (delivery settles inside the run loop before
			// the run can end, and Detach runs after that), so it can be recalled
			// directly.
			if q.rt != nil && !q.rt.RetractSteer(q.steers[idx].Seq) {
				skipSteers = true
				continue
			}
			laneEntries = &q.steers
```

（`newestLocked` 对 steer 车道返回的 `idx` 就是 `len(q.steers)-1`。）

**4c. 改写 `pkg/run/turn_input_test.go` 的 Q4 测试**（135-191 行）：

- 把测试上方注释第 2-3 行「A steer the runtime can no longer retract is skipped — and because the runtime drains FIFO, so is the whole steer lane.」改为「A steer the model is already answering is skipped — and because the runtime delivers FIFO, so is the whole steer lane.」
- 把子测试 `"unretractable steer is skipped"`（166-185 行）整体替换为下面两个子测试：

```go
	t.Run("in-flight steer is retracted from its delivery", func(t *testing.T) {
		q := NewInputQueue()
		rt := NewTurnInputRuntime()
		q.Attach(rt)
		q.FollowUp(Input{Text: "older follow-up"})
		q.Steer(Input{Text: "in flight"})
		// The runtime took the steer for a model call that has not begun
		// answering: it still shows in the queue, so it is still the user's.
		delivery := rt.BeginSteerDelivery(context.Background())
		if delivery == nil {
			t.Fatal("expected an in-flight delivery")
		}
		in, ok := q.Recall()
		if !ok || in.Text != "in flight" {
			t.Fatalf("recall = %#v, %v", in, ok)
		}
		if delivery.Commit() {
			t.Fatal("the call that carried a recalled steer must not commit it")
		}
	})
	t.Run("steer the model is answering is skipped", func(t *testing.T) {
		q := NewInputQueue()
		rt := NewTurnInputRuntime()
		q.Attach(rt)
		q.FollowUp(Input{Text: "older follow-up"})
		q.Steer(Input{Text: "being answered"})
		// Stand in for the instant between the commit and the queue hearing
		// of it: the steer still shows, but the model is answering it.
		rt.SetChangeHook(nil)
		delivery := rt.BeginSteerDelivery(context.Background())
		if delivery == nil || !delivery.Commit() {
			t.Fatal("expected a committed delivery")
		}
		in, ok := q.Recall()
		if !ok || in.Text != "older follow-up" {
			t.Fatalf("recall = %#v, %v", in, ok)
		}
	})
```

**4d. 在 `pkg/run/orchestration_llm_test.go` 追加编排层测试**（放在 `TestToolOrchestrationConsumesPendingSteerWhenTurnHasNoToolCall` 之后；该文件已 import `errors`、`time`、`strings`、`event`、`llm`、`toolpkg`）：

```go
// retractDuringCallLLM answers the first sampling with a tool call. On the
// second — the one carrying the queued steers — it stands in for the user
// pressing the recall key while the request waits on the provider: it recalls
// from the conversation queue, then waits for the abort that recall triggers.
// Every later sampling finishes the turn.
type retractDuringCallLLM struct {
	queue    *InputQueue
	recalled []Input
	calls    [][]llm.Message
}

func (m *retractDuringCallLLM) Execute(ctx context.Context, messages []llm.Message, _ []*llm.Tool) (*llm.Result, error) {
	m.calls = append(m.calls, append([]llm.Message(nil), messages...))
	switch len(m.calls) {
	case 1:
		msg := llm.AssistantMessage(nil, llm.ToolCall{ID: "call-read", Type: llm.ToolTypeFunction,
			Function: llm.FunctionCall{Name: "read_file", Arguments: `{"file_path":"server.go"}`}})
		return &llm.Result{Message: &msg, Usage: &llm.Usage{}}, nil
	case 2:
		in, ok := m.queue.Recall()
		if !ok {
			return nil, errors.New("recall refused a steer that is still in the queue")
		}
		m.recalled = append(m.recalled, in)
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(5 * time.Second):
			return nil, errors.New("recalling an in-flight steer did not abort the call carrying it")
		}
	default:
		msg := llm.AssistantMessage([]llm.ContentPart{llm.Text("done")})
		return &llm.Result{Message: &msg, Usage: &llm.Usage{}}, nil
	}
}

func readFileTestTool(t *testing.T) *llm.Tool {
	t.Helper()
	type readInput struct {
		FilePath string `json:"file_path"`
	}
	readTool, err := llm.NewTool("read_file", "read", func(_ context.Context, _ *readInput) (string, error) {
		return "package main", nil
	})
	if err != nil {
		t.Fatalf("read_file tool: %v", err)
	}
	return readTool
}

// A steer taken for a model call that has not begun answering still shows in
// the queue, so recall must hand it back — and the model must never see it:
// the call carrying it is abandoned and re-sent with only the steers left.
func TestRetractedInFlightSteerIsResentWithoutIt(t *testing.T) {
	st := toolpkg.NewState(t.TempDir())
	st.RegisterToolMeta(event.ToolMeta{Name: "read_file", ReadOnly: true, ConcurrencySafe: true})
	q := NewInputQueue()
	rt := NewTurnInputRuntime()
	q.Attach(rt)
	q.Steer(Input{Text: "keep the old API", Parts: []llm.ContentPart{llm.Text("keep the old API")}})
	q.Steer(Input{Text: "never mind", Parts: []llm.ContentPart{llm.Text("never mind")}})

	inner := &retractDuringCallLLM{queue: q}
	res, err := wrapToolOrchestrationLLM(inner, st).Execute(
		WithTurnInputRuntime(context.Background(), rt),
		[]llm.Message{llm.UserMessage(llm.Text("go"))},
		[]*llm.Tool{readFileTestTool(t)},
	)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res == nil || res.Message == nil || res.Message.TextContent() != "done" {
		t.Fatalf("unexpected result: %#v", res)
	}
	if len(inner.recalled) != 1 || inner.recalled[0].Text != "never mind" {
		t.Fatalf("recalled %#v, want the newest steer", inner.recalled)
	}
	if len(inner.calls) != 3 {
		t.Fatalf("model calls=%d want 3 (tool call, abandoned call, re-sent call)", len(inner.calls))
	}
	third := inner.calls[2]
	if last := third[len(third)-1]; last.Role != llm.RoleUser || last.TextContent() != "keep the old API" {
		t.Fatalf("re-sent call must end with the steer left, got %v", roles(third))
	}
	for _, msg := range append(append([]llm.Message(nil), third...), res.Session...) {
		if msg.TextContent() == "never mind" {
			t.Fatal("the retracted steer reached the model or the persisted session")
		}
	}
	if delivered := q.TakeDelivered(); len(delivered) != 1 || delivered[0].Text != "keep the old API" {
		t.Fatalf("delivered = %#v, want only the steer the model answered", delivered)
	}
	if preview := q.Preview(); preview.Visible() {
		t.Fatalf("queue not empty: %#v", preview)
	}
}

// retractAtAnswerLLM ends the turn without a tool call — the branch that
// reopens it for a queued steer — and then, on the sampling that carries the
// steer, recalls it the way the user's recall key would.
type retractAtAnswerLLM struct {
	queue *InputQueue
	calls int
}

func (m *retractAtAnswerLLM) Execute(ctx context.Context, _ []llm.Message, _ []*llm.Tool) (*llm.Result, error) {
	m.calls++
	if m.calls == 1 {
		msg := llm.AssistantMessage([]llm.ContentPart{llm.Text("first answer")})
		return &llm.Result{Message: &msg, Usage: &llm.Usage{InputTokens: 10, OutputTokens: 2}}, nil
	}
	if _, ok := m.queue.Recall(); !ok {
		return nil, errors.New("recall refused a steer that is still in the queue")
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-time.After(5 * time.Second):
		return nil, errors.New("recalling an in-flight steer did not abort the call carrying it")
	}
}

// When every steer that reopened a finished turn is retracted, the turn ends
// on the answer it had already reached — once, and counted once.
func TestRetractingEveryReopeningSteerEndsTurnOnTheAnswer(t *testing.T) {
	st := toolpkg.NewState(t.TempDir())
	q := NewInputQueue()
	rt := NewTurnInputRuntime()
	q.Attach(rt)
	q.Steer(Input{Text: "actually, also do X", Parts: []llm.ContentPart{llm.Text("actually, also do X")}})

	inner := &retractAtAnswerLLM{queue: q}
	res, err := wrapToolOrchestrationLLM(inner, st).Execute(
		WithTurnInputRuntime(context.Background(), rt),
		[]llm.Message{llm.UserMessage(llm.Text("go"))},
		nil,
	)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if inner.calls != 2 {
		t.Fatalf("model calls=%d want 2 (the answer, then the abandoned call)", inner.calls)
	}
	if res == nil || res.Message == nil || res.Message.TextContent() != "first answer" {
		t.Fatalf("the turn must end on the answer it had reached, got %#v", res)
	}
	if len(res.Session) != 2 || res.Session[1].TextContent() != "first answer" {
		t.Fatalf("session = %v, want the request and the answer once", roles(res.Session))
	}
	if res.Usage == nil || res.Usage.InputTokens != 10 {
		t.Fatalf("usage = %#v, want the answer counted once", res.Usage)
	}
	if delivered := q.TakeDelivered(); len(delivered) != 0 {
		t.Fatalf("a retracted steer was rendered as sent: %#v", delivered)
	}
	if rt.HasSteers() || q.Preview().Visible() {
		t.Fatal("the retracted steer is still queued")
	}
}

// answerThenRecallLLM begins answering the call that carries the steer — the
// response-start boundary — and only then tries to recall it.
type answerThenRecallLLM struct {
	queue      *InputQueue
	calls      int
	recalledOK bool
}

func (m *answerThenRecallLLM) Execute(ctx context.Context, _ []llm.Message, _ []*llm.Tool) (*llm.Result, error) {
	m.calls++
	if m.calls == 1 {
		msg := llm.AssistantMessage(nil, llm.ToolCall{ID: "call-read", Type: llm.ToolTypeFunction,
			Function: llm.FunctionCall{Name: "read_file", Arguments: `{"file_path":"server.go"}`}})
		return &llm.Result{Message: &msg, Usage: &llm.Usage{}}, nil
	}
	if sink := llm.StreamSinkFrom(ctx); sink != nil && sink.OnResponseStarted != nil {
		sink.OnResponseStarted()
	}
	_, m.recalledOK = m.queue.Recall()
	msg := llm.AssistantMessage([]llm.ContentPart{llm.Text("done")})
	return &llm.Result{Message: &msg, Usage: &llm.Usage{}}, nil
}

// Once the model has begun answering a steer it has left the queue and can no
// longer be recalled: the user sees it as a sent message instead.
func TestSteerCannotBeRecalledOnceTheModelAnswersIt(t *testing.T) {
	st := toolpkg.NewState(t.TempDir())
	st.RegisterToolMeta(event.ToolMeta{Name: "read_file", ReadOnly: true, ConcurrencySafe: true})
	q := NewInputQueue()
	rt := NewTurnInputRuntime()
	q.Attach(rt)
	q.Steer(Input{Text: "too late", Parts: []llm.ContentPart{llm.Text("too late")}})
	inner := &answerThenRecallLLM{queue: q}
	ctx := llm.WithStreamSink(WithTurnInputRuntime(context.Background(), rt), &llm.StreamSink{})
	if _, err := wrapToolOrchestrationLLM(inner, st).Execute(ctx,
		[]llm.Message{llm.UserMessage(llm.Text("go"))}, []*llm.Tool{readFileTestTool(t)}); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if inner.recalledOK {
		t.Fatal("a steer the model is answering was handed back for editing")
	}
	if delivered := q.TakeDelivered(); len(delivered) != 1 || delivered[0].Text != "too late" {
		t.Fatalf("delivered = %#v, want the answered steer", delivered)
	}
}

// recallThenLeakLLM recalls the in-flight steer, then behaves like a provider
// whose first output event raced the abort: it fires the response-start
// boundary and a delta, and even returns a complete answer. None of that may
// reach the surface or the session.
type recallThenLeakLLM struct {
	queue *InputQueue
	calls int
}

func (m *recallThenLeakLLM) Execute(ctx context.Context, _ []llm.Message, _ []*llm.Tool) (*llm.Result, error) {
	m.calls++
	sink := llm.StreamSinkFrom(ctx)
	switch m.calls {
	case 1:
		msg := llm.AssistantMessage(nil, llm.ToolCall{ID: "call-read", Type: llm.ToolTypeFunction,
			Function: llm.FunctionCall{Name: "read_file", Arguments: `{"file_path":"server.go"}`}})
		return &llm.Result{Message: &msg, Usage: &llm.Usage{}}, nil
	case 2:
		if _, ok := m.queue.Recall(); !ok {
			return nil, errors.New("recall refused a steer that is still in the queue")
		}
		sink.OnResponseStarted()
		sink.OnDelta("answer to the retracted steer")
		msg := llm.AssistantMessage([]llm.ContentPart{llm.Text("answer to the retracted steer")})
		return &llm.Result{Message: &msg, Usage: &llm.Usage{}}, nil
	default:
		sink.OnResponseStarted()
		sink.OnDelta("done")
		msg := llm.AssistantMessage([]llm.ContentPart{llm.Text("done")})
		return &llm.Result{Message: &msg, Usage: &llm.Usage{}}, nil
	}
}

func TestAbandonedCallOutputNeverReachesTheSurface(t *testing.T) {
	st := toolpkg.NewState(t.TempDir())
	st.RegisterToolMeta(event.ToolMeta{Name: "read_file", ReadOnly: true, ConcurrencySafe: true})
	q := NewInputQueue()
	rt := NewTurnInputRuntime()
	q.Attach(rt)
	q.Steer(Input{Text: "retract me", Parts: []llm.ContentPart{llm.Text("retract me")}})
	var events []string
	ctx := llm.WithStreamSink(WithTurnInputRuntime(context.Background(), rt), &llm.StreamSink{
		OnResponseStarted: func() { events = append(events, "started") },
		OnDelta:           func(text string) { events = append(events, text) },
	})
	inner := &recallThenLeakLLM{queue: q}
	res, err := wrapToolOrchestrationLLM(inner, st).Execute(ctx,
		[]llm.Message{llm.UserMessage(llm.Text("go"))}, []*llm.Tool{readFileTestTool(t)})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if got, want := strings.Join(events, ","), "started,done"; got != want {
		t.Fatalf("surface saw %q, want %q: the abandoned call's output leaked", got, want)
	}
	if inner.calls != 3 || res == nil || res.Message == nil || res.Message.TextContent() != "done" {
		t.Fatalf("calls=%d result=%#v, want the turn finished by a fresh call", inner.calls, res)
	}
	for _, msg := range res.Session {
		if strings.Contains(msg.TextContent(), "retract") {
			t.Fatalf("the abandoned exchange reached the session: %v", roles(res.Session))
		}
	}
}
```

**4e. 在 `pkg/run/compaction_test.go` 末尾追加压缩交互测试**（该文件 `tool` 包未起别名，叫 `tool`）：

```go
// steerCompactLLM answers the first sampling with a call for a large chunk and
// finishes on every later one, recording what each sampling it served was sent.
// A sampling whose context is already cancelled is refused unrecorded, the way
// a provider refuses an aborted request.
type steerCompactLLM struct {
	calls [][]llm.Message
}

func (m *steerCompactLLM) Execute(ctx context.Context, messages []llm.Message, _ []*llm.Tool) (*llm.Result, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	m.calls = append(m.calls, append([]llm.Message(nil), messages...))
	if len(m.calls) == 1 {
		msg := llm.AssistantMessage(nil, llm.ToolCall{ID: "chunk", Type: llm.ToolTypeFunction,
			Function: llm.FunctionCall{Name: "read_chunk", Arguments: `{}`}})
		return &llm.Result{Message: &msg}, nil
	}
	msg := llm.AssistantMessage([]llm.ContentPart{llm.Text("done")})
	return &llm.Result{Message: &msg}, nil
}

func steerCompactFixture(t *testing.T, steer string) (*tool.State, *llm.Tool, *InputQueue, *TurnInputRuntime) {
	t.Helper()
	st := tool.NewState(t.TempDir())
	st.RegisterToolMeta(event.ToolMeta{Name: "read_chunk", ReadOnly: true, ConcurrencySafe: true})
	readTool, err := llm.NewTool("read_chunk", "read a chunk", func(context.Context, *struct{}) (string, error) {
		return strings.Repeat("large tool result\n", 300), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	q := NewInputQueue()
	rt := NewTurnInputRuntime()
	q.Attach(rt)
	q.Steer(Input{Text: steer, Parts: []llm.ContentPart{llm.Text(steer)}})
	return st, readTool, q, rt
}

// A checkpoint is durable and a queued steer riding on the call is not settled
// yet, so a mid-turn compaction summarizes only the history before the steer
// and the request carries the steer verbatim after the checkpoint.
func TestCompactionKeepsInFlightSteersOutOfTheCheckpoint(t *testing.T) {
	st, readTool, q, rt := steerCompactFixture(t, "also check the tests")
	var compacted [][]llm.Message
	inner := &steerCompactLLM{}
	recoverable := WrapRecoverableLLM(inner, nil, &CompactChainDeps{
		ExplicitLimit: 500,
		TryCompact: func(_ context.Context, messages []llm.Message, _ []*llm.Tool, _ bool) ([]llm.Message, bool, error) {
			compacted = append(compacted, append([]llm.Message(nil), messages...))
			return []llm.Message{llm.UserMessage(llm.Text("checkpoint"))}, true, nil
		},
	})
	ctx := WithTurnInputRuntime(tool.WithRunID(context.Background(), "steer-compaction"), rt)
	res, err := wrapToolOrchestrationLLM(recoverable, st).Execute(ctx,
		[]llm.Message{llm.UserMessage(llm.Text("start"))}, []*llm.Tool{readTool})
	if err != nil {
		t.Fatal(err)
	}
	if len(compacted) != 1 {
		t.Fatalf("compactions=%d want 1", len(compacted))
	}
	if last := compacted[0][len(compacted[0])-1]; last.Role != llm.RoleTool {
		t.Fatalf("the checkpoint covered the in-flight steer: compaction input %v", roles(compacted[0]))
	}
	if second := inner.calls[1]; len(second) != 2 || second[0].TextContent() != "checkpoint" || second[1].TextContent() != "also check the tests" {
		t.Fatalf("second request = %v, want the checkpoint followed by the steer", roles(second))
	}
	if len(res.Session) != 3 || res.Session[1].TextContent() != "also check the tests" || res.Session[2].TextContent() != "done" {
		t.Fatalf("session = %v, want checkpoint, steer, answer", roles(res.Session))
	}
	if delivered := q.TakeDelivered(); len(delivered) != 1 {
		t.Fatalf("delivered = %#v, want the answered steer", delivered)
	}
}

// Recalling the steer while the checkpoint is being written must neither cancel
// the compaction — it never included the steer — nor let the steer reach the
// model: the checkpoint is kept and the call is re-sent without it.
func TestRetractionDuringCompactionKeepsTheCheckpoint(t *testing.T) {
	st, readTool, q, rt := steerCompactFixture(t, "never mind")
	compactions := 0
	var compactCtxErr error
	inner := &steerCompactLLM{}
	recoverable := WrapRecoverableLLM(inner, nil, &CompactChainDeps{
		ExplicitLimit: 500,
		TryCompact: func(ctx context.Context, _ []llm.Message, _ []*llm.Tool, _ bool) ([]llm.Message, bool, error) {
			compactions++
			if _, ok := q.Recall(); !ok {
				t.Error("recall refused a steer that is still in the queue")
			}
			compactCtxErr = ctx.Err()
			return []llm.Message{llm.UserMessage(llm.Text("checkpoint"))}, true, nil
		},
	})
	ctx := WithTurnInputRuntime(tool.WithRunID(context.Background(), "steer-compaction-retract"), rt)
	res, err := wrapToolOrchestrationLLM(recoverable, st).Execute(ctx,
		[]llm.Message{llm.UserMessage(llm.Text("start"))}, []*llm.Tool{readTool})
	if err != nil {
		t.Fatal(err)
	}
	if compactCtxErr != nil {
		t.Fatalf("the retraction cancelled the compaction: %v", compactCtxErr)
	}
	if compactions != 1 {
		t.Fatalf("compactions=%d want 1: the kept checkpoint must not be redone", compactions)
	}
	if len(inner.calls) != 2 || len(inner.calls[1]) != 1 || inner.calls[1][0].TextContent() != "checkpoint" {
		t.Fatalf("requests = %d, last %v; want the re-sent call to carry only the checkpoint", len(inner.calls), roles(inner.calls[len(inner.calls)-1]))
	}
	if len(res.Session) != 2 || res.Session[0].TextContent() != "checkpoint" || res.Session[1].TextContent() != "done" {
		t.Fatalf("session = %v, want checkpoint and answer", roles(res.Session))
	}
	if delivered := q.TakeDelivered(); len(delivered) != 0 {
		t.Fatalf("a retracted steer was rendered as sent: %#v", delivered)
	}
}
```

**Verify**:
- `CGO_ENABLED=1 go test -tags fts5 ./pkg/run -run 'Retract|Steer|Recall|InputQueue|Compact|Overflow|Abandoned' -count=1 -v 2>&1 | grep -E '^(--- FAIL|ok|FAIL)'` → 只有 `ok` 行。
- 新测试逐个确认存在且 PASS：`TestRetractedInFlightSteerIsResentWithoutIt`、`TestRetractingEveryReopeningSteerEndsTurnOnTheAnswer`、`TestSteerCannotBeRecalledOnceTheModelAnswersIt`、`TestAbandonedCallOutputNeverReachesTheSurface`、`TestCompactionKeepsInFlightSteersOutOfTheCheckpoint`、`TestRetractionDuringCompactionKeepsTheCheckpoint`、`TestInputQueueRecallPicksNewestByLane/in-flight_steer_is_retracted_from_its_delivery`、`TestInputQueueRecallPicksNewestByLane/steer_the_model_is_answering_is_skipped`。
- `CGO_ENABLED=1 go test -race -tags fts5 ./pkg/run -run 'Retract|Steer|Recall|InputQueue|Compact|Overflow|Abandoned' -count=1` → `ok`，输出中无 `DATA RACE`。

### Step 5: 「清空队列」同样撤回在途 steer

**5a.** 把 `pkg/run/turn_input.go` 的 `Discard`（doc 注释 + 函数，按函数名定位）整体替换为：

```go
// Discard empties every lane and returns how many messages were dropped.
// Steers are retracted from the attached runtime first — pending, or carried
// by a model call that has not begun answering, which is then abandoned and
// re-sent without them: clearing only the queue would leave the runtime free
// to hand them to the model, with nothing left to render them into the
// transcript. A steer the model is already answering cannot be retracted and
// is not counted — the model has it, so it is not lost. With no runtime
// attached the steers never left the queue, so every one of them is dropped.
func (q *InputQueue) Discard() int {
	if q == nil {
		return 0
	}
	q.mu.Lock()
	dropped := 0
	for _, in := range q.steers {
		if q.rt == nil || q.rt.RetractSteer(in.Seq) {
			dropped++
		}
	}
	dropped += len(q.rejected) + len(q.followUp)
	q.steers, q.rejected, q.followUp, q.delivered = nil, nil, nil, nil
	hook := q.hook
	q.mu.Unlock()
	if hook != nil {
		hook()
	}
	return dropped
}
```

（`RetractSteer` 命中在途交付时会调用 abort；同一交付被撤多条时 abort 被调多次，`context.CancelCauseFunc` 重复调用是无害的空操作。`q.delivered` 照旧清空：切换会话时旧会话已送达、待渲染的 steer 不应画进新会话。）

**5b.** 改写 `pkg/run/turn_input_test.go` 中 `TestInputQueueDiscardCountsWhatItDrops` 的第二段（从 `q2 := NewInputQueue()` 到函数结尾的 `}` 之前），替换为：

```go
	// A steer taken for a model call that has not begun answering is still
	// the user's: discarding retracts it from that call and counts it.
	q2 := NewInputQueue()
	rt2 := NewTurnInputRuntime()
	q2.Attach(rt2)
	q2.Steer(Input{Text: "in flight"})
	delivery := rt2.BeginSteerDelivery(context.Background())
	if delivery == nil {
		t.Fatal("expected an in-flight delivery")
	}
	q2.FollowUp(Input{Text: "follow-up"})
	if dropped := q2.Discard(); dropped != 2 {
		t.Fatalf("dropped = %d, want 2: the in-flight steer was never answered", dropped)
	}
	if delivery.Commit() {
		t.Fatal("the call carrying a discarded steer must not commit it")
	}

	// A steer the model is already answering is out of reach and not counted.
	q3 := NewInputQueue()
	rt3 := NewTurnInputRuntime()
	q3.Attach(rt3)
	q3.Steer(Input{Text: "being answered"})
	// Stand in for the instant between the commit and the queue hearing of it.
	rt3.SetChangeHook(nil)
	if d := rt3.BeginSteerDelivery(context.Background()); d == nil || !d.Commit() {
		t.Fatal("expected a committed delivery")
	}
	q3.FollowUp(Input{Text: "follow-up"})
	if dropped := q3.Discard(); dropped != 1 {
		t.Fatalf("a steer the model is answering must not be counted as dropped, got %d", dropped)
	}

	// With the runtime detached the steers never left the queue: all count.
	q4 := NewInputQueue()
	q4.Attach(NewTurnInputRuntime())
	q4.Steer(Input{Text: "never sent"})
	q4.Detach()
	if dropped := q4.Discard(); dropped != 1 {
		t.Fatalf("dropped = %d, want 1: a steer left behind by a detached runtime was never delivered", dropped)
	}
```

**5c.** 在 `pkg/run/orchestration_llm_test.go` 中 Step 4d 加的测试之后追加：

```go
// discardDuringCallLLM answers the first sampling with a tool call. On the
// second — the one carrying the queued steer — the user leaves the
// conversation, which discards its queue, and the call waits for the abort
// that triggers. Every later sampling finishes the turn.
type discardDuringCallLLM struct {
	queue   *InputQueue
	dropped int
	calls   [][]llm.Message
}

func (m *discardDuringCallLLM) Execute(ctx context.Context, messages []llm.Message, _ []*llm.Tool) (*llm.Result, error) {
	m.calls = append(m.calls, append([]llm.Message(nil), messages...))
	switch len(m.calls) {
	case 1:
		msg := llm.AssistantMessage(nil, llm.ToolCall{ID: "call-read", Type: llm.ToolTypeFunction,
			Function: llm.FunctionCall{Name: "read_file", Arguments: `{"file_path":"server.go"}`}})
		return &llm.Result{Message: &msg, Usage: &llm.Usage{}}, nil
	case 2:
		m.dropped = m.queue.Discard()
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(5 * time.Second):
			return nil, errors.New("discarding an in-flight steer did not abort the call carrying it")
		}
	default:
		msg := llm.AssistantMessage([]llm.ContentPart{llm.Text("done")})
		return &llm.Result{Message: &msg, Usage: &llm.Usage{}}, nil
	}
}

// Leaving a conversation discards its queue. A steer already taken for a model
// call that has not begun answering must go with it — otherwise the outgoing
// run hands it to the model with nothing left to render it into the transcript.
func TestDiscardRetractsTheInFlightSteer(t *testing.T) {
	st := toolpkg.NewState(t.TempDir())
	st.RegisterToolMeta(event.ToolMeta{Name: "read_file", ReadOnly: true, ConcurrencySafe: true})
	q := NewInputQueue()
	rt := NewTurnInputRuntime()
	q.Attach(rt)
	q.Steer(Input{Text: "left behind", Parts: []llm.ContentPart{llm.Text("left behind")}})

	inner := &discardDuringCallLLM{queue: q}
	res, err := wrapToolOrchestrationLLM(inner, st).Execute(
		WithTurnInputRuntime(context.Background(), rt),
		[]llm.Message{llm.UserMessage(llm.Text("go"))},
		[]*llm.Tool{readFileTestTool(t)},
	)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if inner.dropped != 1 {
		t.Fatalf("dropped = %d, want 1: the in-flight steer was never answered", inner.dropped)
	}
	if len(inner.calls) != 3 {
		t.Fatalf("model calls=%d want 3 (tool call, abandoned call, re-sent call)", len(inner.calls))
	}
	if third := inner.calls[2]; third[len(third)-1].Role != llm.RoleTool {
		t.Fatalf("re-sent call must end at the tool result, got %v", roles(third))
	}
	if res == nil || res.Message == nil || res.Message.TextContent() != "done" {
		t.Fatalf("unexpected result: %#v", res)
	}
	for _, msg := range res.Session {
		if msg.TextContent() == "left behind" {
			t.Fatal("the discarded steer reached the persisted session")
		}
	}
	if delivered := q.TakeDelivered(); len(delivered) != 0 {
		t.Fatalf("a discarded steer was rendered as sent: %#v", delivered)
	}
}
```

**Verify**:
- `CGO_ENABLED=1 go test -tags fts5 ./pkg/run -run 'TestInputQueueDiscardCountsWhatItDrops|TestDiscardRetractsTheInFlightSteer|TestSessionSwitch|Subagent' -count=1` → `ok`
- `CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -run 'TestSessionSwitch' -count=1` → `ok`（`TestSessionSwitchRetractsQueuedSteersFromRuntime`、`TestSessionSwitchReportsDiscardedQueueToUser`、`TestSessionSwitchStaysQuietWhenNothingWasQueued` 不受影响）
- `CGO_ENABLED=1 go test -race -tags fts5 ./pkg/run -run 'Discard' -count=1` → `ok`，无 `DATA RACE`

### Step 6: gateway 撤回在途 steer（G1，只补测试）

在 `pkg/gateway/run_control_test.go` 的 `TestHandleRunQueuedInputEditLastRetractsPendingSteer` 之后追加（文件已 import `context`、`encoding/json`、`net/http`、`net/http/httptest`、`strings`、`require`、`run`）：

```go
// A steer the run took for a model call that has not begun answering still
// shows in the page's queue, so edit_last must hand it back — and the call
// carrying it must not commit it.
func TestHandleRunQueuedInputEditLastRetractsInFlightSteer(t *testing.T) {
	s := &Server{}
	runID := "run-edit-in-flight"
	q := queue(t, s, runID)

	req := httptest.NewRequest(http.MethodPost, "/api/runs/"+runID+"/input", strings.NewReader(`{"message":"please adjust"}`))
	req.SetPathValue("id", runID)
	rr := httptest.NewRecorder()
	s.handleRunInput(rr, req)
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())

	delivery := q.Runtime().BeginSteerDelivery(context.Background())
	require.NotNil(t, delivery, "the run takes the steer for its next model call")

	req = httptest.NewRequest(http.MethodPost, "/api/runs/"+runID+"/queued-input", strings.NewReader(`{"action":"edit_last"}`))
	req.SetPathValue("id", runID)
	rr = httptest.NewRecorder()
	s.handleRunQueuedInput(rr, req)
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())

	var resp struct {
		Accepted bool   `json:"accepted"`
		Message  string `json:"message"`
		Preview  struct {
			PendingSteers []string `json:"pending_steers"`
		} `json:"preview"`
	}
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp))
	require.True(t, resp.Accepted, "a steer still shown in the queue must come back")
	require.Equal(t, "please adjust", resp.Message)
	require.Empty(t, resp.Preview.PendingSteers)
	require.True(t, delivery.Retracted())
	require.False(t, delivery.Commit(), "the call that carried a recalled steer must not commit it")
}
```

**Verify**: `CGO_ENABLED=1 go test -tags fts5 ./pkg/gateway -run 'TestHandleRunQueuedInput' -count=1` → `ok`。

### Step 7: subagent 队列的每次变化都推给 surface（G2）

**7a. `pkg/run/subagent.go`**：

1. `SubagentSurface`（568-577 行）在 `OnBoundary` 之后加字段：

```go
	// OnQueueChanged receives the subagent's queue preview after every change
	// to it — a message queued, recalled, handed to the model, discarded, or
	// settled when an execution ends — so a surface that does not read the
	// queue on every paint can republish what its view shows. It can be called
	// while the channel's own lock is held (a withdrawal empties the queue
	// under it), so it must not call back into the channel: publishing the
	// preview is all it may do.
	OnQueueChanged func(agentKey string, preview QueuePreview)
```

2. `subagentChannel` 结构体在 `surface SubagentSurface` 之后加：

```go
	// queueHookMu guards onQueueChanged apart from mu: the queue's change hook
	// fires from queue operations that run while mu is held (withdraw), so it
	// must not take mu.
	queueHookMu    sync.Mutex
	onQueueChanged func(agentKey string, preview QueuePreview)
```

3. `subagentChannelFor`（646 行起）创建通道时，在 `ch.queue = control.SessionQueue(workerSessionID)` 的下一行加 `ch.queue.SetChangeHook(ch.queueChanged)`（仍在 `if control := ...` 块内，只在新建通道时执行一次）。

4. `setSurface`（676-680 行）改为：

```go
func (ch *subagentChannel) setSurface(surface SubagentSurface) {
	ch.mu.Lock()
	ch.surface = surface
	ch.mu.Unlock()
	ch.queueHookMu.Lock()
	ch.onQueueChanged = surface.OnQueueChanged
	ch.queueHookMu.Unlock()
}
```

5. 在 `setSurface` 之后新增：

```go
// queueChanged hands the surface the subagent's queue as its view shows it.
// It is the queue's change hook, so it runs after the queue has released its
// own lock.
func (ch *subagentChannel) queueChanged() {
	ch.queueHookMu.Lock()
	notify := ch.onQueueChanged
	ch.queueHookMu.Unlock()
	if notify != nil {
		notify(ch.agentKey, ch.queue.Preview())
	}
}
```

先确认：`grep -rn "SetChangeHook(" pkg --include='*.go' | grep -v _test` 改动前只命中 `pkg/tui/chat_session.go`（会话队列）、`pkg/gateway/server.go`、`pkg/gateway/run_control.go`（run 的会话队列）与 `pkg/run/turn_input.go` 的定义——没有任何代码给 subagent 的 worker 会话队列装 hook。

**7b. `pkg/run/subagent_test.go`**，在 `TestSubagentViewQueueQuestionsAnswerFromTheLiveChannel` 之后追加：

```go
// A surface that does not read a subagent's queue on every paint — the web —
// must hear of every change to it, or its view keeps showing a message the
// model already has, or one the queue settled, as still queued and recallable.
func TestSubagentQueueChangesReachTheSurface(t *testing.T) {
	fix := newPersistenceFixture(t, "worker system")
	owner := fix.fac.Owner
	owner.Control = NewController()
	owner.Events = &channelEventProbe{}
	release := make(chan struct{})
	owner.SubagentExecutor = &channelTestExecutor{store: fix.store, hold: true, release: release, answer: "x"}
	var mu sync.Mutex
	var previews []QueuePreview
	boundary := make(chan struct{}, 1)
	surface := SubagentSurface{
		OnBoundary: func(string, []Input, []Input) { boundary <- struct{}{} },
		OnQueueChanged: func(agentKey string, preview QueuePreview) {
			if agentKey != "task-hook" {
				t.Errorf("OnQueueChanged key = %q, want the roster key", agentKey)
			}
			mu.Lock()
			previews = append(previews, preview)
			mu.Unlock()
		},
	}
	last := func() QueuePreview {
		mu.Lock()
		defer mu.Unlock()
		if len(previews) == 0 {
			return QueuePreview{}
		}
		return previews[len(previews)-1]
	}
	conv := "conv-queue-hook"
	worker := "main:conv-queue-hook:worker:00000000-0000-0000-0000-0000000005bb"
	fix.seedWorker(t, conv, worker, []llm.Message{llm.UserMessage(llm.Text("seed"))})
	seedChannelRecord(t, fix.fac, agent.HistoryEntry{
		TaskID: "task-hook", RunID: "seed-run", ParentRunID: "parent-1", SessionID: conv, WorkerSessionID: worker,
		Task: "work", Status: agent.StatusOK, AgentType: "explore", AgentKind: "typed", RuntimeKind: "typed_subagent",
		Continuable: true, StartedAt: 10, UpdatedAt: 20, FinishedAt: 20,
	})
	send := func(text string, mode TurnInputMode, want SubagentDelivery) {
		t.Helper()
		if got, err := SendToSubagent(context.Background(), owner, surface, conv, "task-hook", Input{Text: text}, mode); err != nil || got != want {
			t.Fatalf("send %q = %q, %v; want %q", text, got, err, want)
		}
	}
	send("start", TurnInputModeSteer, SubagentDeliveryStarted)
	waitSubagentBusy(t, owner, conv, "task-hook")
	send("one", TurnInputModeSteer, SubagentDeliverySteered)
	send("two", TurnInputModeSteer, SubagentDeliverySteered)
	if got := strings.Join(last().Steers, ","); got != "one,two" {
		t.Fatalf("surface preview steers = %q, want one,two", got)
	}

	// The model takes both steers and begins answering: the view must drop them.
	delivery := owner.Control.SessionQueue(worker).Runtime().BeginSteerDelivery(context.Background())
	if delivery == nil || !delivery.Commit() {
		t.Fatal("expected the running execution to take the steers")
	}
	if p := last(); len(p.Steers) != 0 {
		t.Fatalf("after delivery the surface still shows %v as queued", p.Steers)
	}

	send("later", TurnInputModeFollowUp, SubagentDeliveryQueued)
	if got := strings.Join(last().FollowUp, ","); got != "later" {
		t.Fatalf("surface preview follow-ups = %q, want later", got)
	}
	if in, ok := RecallSubagentInput(owner, conv, "task-hook"); !ok || in.Text != "later" {
		t.Fatalf("RecallSubagentInput = %+v, %v; want later", in, ok)
	}
	if p := last(); p.Visible() {
		t.Fatalf("after recall the surface still shows %+v", p)
	}

	// A follow-up the execution's end settles leaves the view as well.
	send("after", TurnInputModeFollowUp, SubagentDeliveryQueued)
	close(release)
	select {
	case <-boundary:
	case <-time.After(5 * time.Second):
		t.Fatal("the execution never reached its boundary")
	}
	if p := last(); p.Visible() {
		t.Fatalf("after the execution ended the surface still shows %+v", p)
	}
	waitSubagentIdle(t, owner, conv, "task-hook")
}
```

（先确认 `pkg/run/subagent_test.go` 已 import `strings`、`sync`、`time`、`agent`、`llm`；缺哪个补哪个。`SendToSubagent` 对运行中的 subagent：`TurnInputModeSteer` 返回 `SubagentDeliverySteered`，其它模式返回 `SubagentDeliveryQueued`，见 `pkg/run/subagent.go:1902-1910`。）

**7c. `pkg/gateway/api_extra.go`**：

1. 把 `publishSubagentPendingInputUpdated`（1755-1772 行）改为委托给新的 `publishSubagentPreview`：

```go
// publishSubagentPendingInputUpdated reports a subagent's queue as its own view
// shows it, read from the live channel.
func (s *Server) publishSubagentPendingInputUpdated(ctx context.Context, sid, agentKey string, runner *run.Runner) {
	if runner == nil {
		return
	}
	s.publishSubagentPreview(ctx, sid, agentKey, subagentInputPreview(runner, sid, agentKey))
}

// publishSubagentPreview reports a subagent's queue as its own view shows it,
// tagged with its roster key so it lands there and nowhere else.
func (s *Server) publishSubagentPreview(ctx context.Context, sid, agentKey string, preview turn.PendingInputPreview) {
	if s == nil || s.RunEvents() == nil {
		return
	}
	payload := event.PendingInputUpdatedPayload{
		AgentID:        strings.TrimSpace(agentKey),
		PendingSteers:  preview.PendingSteers,
		RejectedSteers: preview.RejectedSteers,
		QueuedMessages: preview.QueuedMessages,
	}
	if err := s.RunEvents().Publish(ctx, event.NewRunEvent("", "", strings.TrimSpace(sid), event.RunEventPendingInputUpdated, payload, time.Now())); err != nil {
		slog.Error("publish subagent pending input", "session_id", sid, "agent_id", agentKey, "err", err)
	}
}
```

2. `subagentConversationSurface`（1724-1734 行）在 `OnBoundary` 之后加：

```go
		// Every change to the subagent's queue reaches its open views: the
		// web reads the queue only from these events, so a message the model
		// took, or one the queue settled, would otherwise stay on screen as
		// queued and recallable.
		OnQueueChanged: func(agentKey string, preview run.QueuePreview) {
			s.publishSubagentPreview(context.Background(), sid, agentKey, toPendingInputPreview(preview))
		},
```

（`toPendingInputPreview` 在同包的 `run_control.go:28-34`。HTTP 处理函数里已有的 `publishSubagentPendingInputUpdated` 调用保留：surface 可能是由别的入口设置的，多发一次相同的预览是幂等的。）

**7d. `pkg/gateway/api_extra_test.go`** 末尾追加：

```go
// The engine tells the web's subagent surface about every change to that
// subagent's queue; the surface republishes the preview, so the open view
// stops showing a message the model already has.
func TestSubagentSurfacePublishesQueueChanges(t *testing.T) {
	s := newRunInputTestServer(t)
	surface := s.subagentConversationSurface("sid")
	require.NotNil(t, surface.OnQueueChanged)
	surface.OnQueueChanged("agent-1", run.QueuePreview{Steers: []string{"still queued"}})

	events, err := s.RunRT.ListSessionEventsOfType(context.Background(), "sid", event.RunEventPendingInputUpdated, 10)
	require.NoError(t, err)
	require.Len(t, events, 1)
	var payload event.PendingInputUpdatedPayload
	require.NoError(t, json.Unmarshal(events[0].Payload, &payload))
	require.Equal(t, "agent-1", payload.AgentID)
	require.Equal(t, []string{"still queued"}, payload.PendingSteers)
}
```

（`newRunInputTestServer` 在同包的 `run_control_test.go`，它建好了会话 `sid`。）

**Verify**:
- `CGO_ENABLED=1 go test -tags fts5 ./pkg/run -run 'TestSubagent' -count=1` → `ok`
- `CGO_ENABLED=1 go test -race -tags fts5 ./pkg/run -run 'TestSubagent' -count=1` → `ok`，无 `DATA RACE`，无超时
- `CGO_ENABLED=1 go test -tags fts5 ./pkg/gateway -run 'TestSubagent' -count=1` → `ok`

### Step 8: gateway 把送达的 steer 作为事件发出（G3 后端）

**8a. `pkg/event/run_events.go`**：在 `RunEventQueuedInputReleased` 下一行加常量：

```go
	// RunEventInputDelivered reports a steer the run handed its model at a
	// tool boundary: the user's own message, which a surface draws into the
	// conversation where the model received it — between what the run said
	// before it and what it says in answer — the way the terminal draws it
	// and where the transcript keeps it.
	RunEventInputDelivered = "input_delivered"
```

并在 `SubagentInputDeliveredPayload`（352-359 行）之后加：

```go
// InputDeliveredPayload is a steer the run handed its model: the message as
// its sender wrote it, with what it attached.
type InputDeliveredPayload struct {
	Text          string   `json:"text"`
	Attachments   []string `json:"attachments,omitempty"`
	MentionImages []string `json:"mention_images,omitempty"`
}
```

**8b. `pkg/gateway/run_control.go`**：在 `appendPendingInputUpdated`（250-260 行）之后新增：

```go
// watchRunQueue republishes a run's queue after every change to it, and draws
// each steer the run hands its model into the conversation as the user's own
// message, ahead of the preview that no longer lists it. The delivered list is
// drained here — the way the terminal takes it to render — so each steer is
// published exactly once and the conversation's queue does not keep growing.
func (s *Server) watchRunQueue(q *run.InputQueue, runID, sessionID string) {
	if s == nil || q == nil {
		return
	}
	q.SetChangeHook(func() {
		ctx := context.Background()
		for _, in := range q.TakeDelivered() {
			_ = s.publishGatewayRunEvent(ctx, sessionID, runID, event.RunEventInputDelivered, event.InputDeliveredPayload{
				Text:          in.Text,
				Attachments:   in.Attachments,
				MentionImages: in.MentionImages,
			})
		}
		s.appendPendingInputUpdated(ctx, runID, sessionID, toPendingInputPreview(q.Preview()))
	})
}
```

然后把两处重复的 hook 换成它：
- `pkg/gateway/server.go:1706-1709`：

```go
			q.SetChangeHook(func() {
				s.appendPendingInputUpdated(context.Background(), runID, sid, toPendingInputPreview(q.Preview()))
			})
```

改为 `s.watchRunQueue(q, runID, sid)`。
- `pkg/gateway/run_control.go:668-671`（`startDetachedTurn` 里同样的三行）改为 `s.watchRunQueue(q, runID, sid)`。

**8c. `pkg/gateway/run_control_test.go`**：

1. 测试辅助 `trackedQueue`（约 655-667 行）里的手写 hook：

```go
	q.SetChangeHook(func() {
		s.appendPendingInputUpdated(context.Background(), runID, sid, toPendingInputPreview(q.Preview()))
	})
```

改为 `s.watchRunQueue(q, runID, sid)`，让测试跑的就是生产的 hook。

2. 在 `trackedQueue` 之后追加：

```go
// A steer the run hands its model is drawn into the conversation as the user's
// own message, ahead of the queue preview that no longer lists it — and only
// once, however often the queue changes afterwards.
func TestRunQueuePublishesDeliveredSteerOnce(t *testing.T) {
	s := newRunInputTestServer(t)
	runID := "run-delivered"
	q := trackedQueue(t, s, runID)
	require.True(t, q.Steer(run.Input{Text: "also run lint", Attachments: []string{"file-1"}}))

	delivery := q.Runtime().BeginSteerDelivery(context.Background())
	require.NotNil(t, delivery)
	require.True(t, delivery.Commit())
	require.True(t, q.FollowUp(run.Input{Text: "a later change to the queue"}))

	events, err := s.RunRT.ListRunEvents(context.Background(), runID, 100)
	require.NoError(t, err)
	var order []string
	var delivered []event.InputDeliveredPayload
	for _, evt := range events {
		switch evt.Type {
		case event.RunEventInputDelivered:
			var p event.InputDeliveredPayload
			require.NoError(t, json.Unmarshal(evt.Payload, &p))
			delivered = append(delivered, p)
			order = append(order, "delivered")
		case event.RunEventPendingInputUpdated:
			order = append(order, "preview")
		}
	}
	require.Len(t, delivered, 1, "each delivered steer is drawn once")
	require.Equal(t, "also run lint", delivered[0].Text)
	require.Equal(t, []string{"file-1"}, delivered[0].Attachments)
	at := -1
	for i, kind := range order {
		if kind == "delivered" {
			at = i
		}
	}
	require.True(t, at >= 0 && at+1 < len(order) && order[at+1] == "preview",
		"the message is drawn before the preview that drops it: %v", order)
	require.Empty(t, q.TakeDelivered(), "the hook drains what it publishes")
}
```

**8d. `frontend/src/lib/forebrainGatewayRuntime.ts`**：在 `ForebrainRunEventType` 联合类型里 `| 'subagent_input_delivered'`（第 25 行）下一行加 `| 'input_delivered'`。

**Verify**:
- `grep -n "SetChangeHook" pkg/gateway/*.go` → 只命中 `run_control.go` 里 `watchRunQueue` 的一处
- `CGO_ENABLED=1 go test -tags fts5 ./pkg/gateway -count=1` → `ok`（含既有的 `pendingInputSteps` 系列测试）
- `go vet ./pkg/gateway/... ./pkg/event/...` → 无输出

### Step 9: web 把送达的 steer 画成独立的用户气泡（G3 前端）

全部改动在 `frontend/src/composables/useChatStream.ts`。

**9a.** `RunProjectionState`（1236-1246 行）在 `released?: ReleasedInput[]` 之后加字段；并在该 interface 之后新增模块级函数：

```ts
  /**
   * The answer the run's output is drawn into once a steer has been handed to
   * its model: each delivered steer closes the answer so far and opens a new
   * one after it. Unset until the first, when the output goes to the answer
   * the run started with.
   */
  answerMessageId?: string
```

```ts
/** The message a run's output is drawn into now (see RunProjectionState.answerMessageId). */
function answerTarget(state: RunProjectionState, startedWith: string): string {
  return state.answerMessageId || startedWith
}
```

**9b.** `transcriptBackedEventTypes`（2373-2380 行）的集合里加一项 `'input_delivered',`。在该项上方加注释：`// A delivered steer is a user row of the transcript once its run is persisted.`

**9c.** `handleRunEvent`（3408 行起）：在 `const payload = evt.payload ?? {}` 的下一行插入：

```ts
    // A run's output lands in its latest answer: the one opened after the
    // newest steer the run handed its model (see input_delivered), or the
    // answer the run started with.
    assistantMessageId = answerTarget(state, assistantMessageId)
```

**9d.** 在 `handleRunEvent` 主会话的 `switch (evt.type)` 里，`case 'pending_input_updated':`（约 3698 行，`applyPendingInputPreview(payload)` 那个，不是 subagent 分支 2917 行的那个）之前加：

```ts
      case 'input_delivered': {
        // A steer the run handed its model is the user's own message, drawn
        // where the model received it: the answer so far closes, the message
        // follows it, and what the run says next opens a new answer after it —
        // the shape a reload draws from the transcript, and the place the
        // terminal draws it live.
        const delivered: ReleasedInput = {
          text: String(payload.text ?? ''),
          attachments: stringList(payload.attachments),
          mentionImages: stringList(payload.mentionImages),
        }
        if (!delivered.text.trim() && delivered.attachments.length === 0 && delivered.mentionImages.length === 0) return
        const seed = eventID || `${Date.now()}`
        const userId = `user-delivered-${seed}`
        const nextAnswerId = `assistant-delivered-${seed}`
        const runId = String(evt.runId ?? '').trim() || undefined
        const inserted: ChatMessage[] = [
          { id: userId, role: 'user', content: delivered.text, runId },
          { id: nextAnswerId, role: 'assistant', content: NOTHING_SAID_YET, runId },
        ]
        const index = messages.value.findIndex((message) => message.id === assistantMessageId)
        if (index >= 0) {
          const closing = messages.value[index]!
          const blocks = closeStreamedBlocks(closing.blocks ?? [], ['assistant', 'thinking'], evt.createdAt)
          messages.value = [
            ...messages.value.slice(0, index),
            { ...closing, blocks, content: spokenText(blocks) },
            ...inserted,
            ...messages.value.slice(index + 1),
          ]
        } else {
          messages.value = [...messages.value, ...inserted]
        }
        state.answerMessageId = nextAnswerId
        // The answer text the run accumulates is the new answer's alone now.
        state.fullAnswer = ''
        answer.value = ''
        if (delivered.attachments.length || delivered.mentionImages.length) {
          // What it attached is described by name once the gateway answers.
          void releasedSubmission(delivered).then((submission) => {
            const attachments = messageAttachments(submission)
            updateMessageById(userId, (message) => ({
              ...message,
              content: message.content.trim() ? message.content : attachedOnlyText(attachments),
              attachments,
            }))
          })
        }
        return
      }
```

（`stringList`、`attachedOnlyText`、`messageAttachments`、`closeStreamedBlocks`、`spokenText`、`NOTHING_SAID_YET` 是模块级函数/常量；`releasedSubmission`、`updateMessageById`、`messages`、`answer` 在 `useChatStream` 作用域里。`eventID` 是 `handleRunEvent` 开头已有的局部变量。）

**9e.** `send()` 里本 run 的收尾代码改为落到当前回答。在 `const eventState: RunProjectionState = { ... }`（约 4003 行）的下一行加：

```ts
    // The answer this send's run is drawing into now (see answerTarget).
    const answerId = () => answerTarget(eventState, assistantMessageId)
```

然后把下列位置的 `assistantMessageId` 换成 `answerId()`（只换这些）：
- `op === 'run_completed'` 分支的 `updateMessageById(assistantMessageId, ...)`（约 4098 行）
- `op === 'run_error'` 分支的 `updateMessageById(assistantMessageId, ...)`（约 4115 行）
- `op === 'run_cancelled'` 分支的 `updateMessageById(assistantMessageId, ...)`（约 4156 行）
- run 结束后的收尾：`const currentAssistant = messages.value.find((message) => message.id === assistantMessageId)`（约 4370 行）、紧随其后的 `filter((message) => message.id !== assistantMessageId)`（约 4381 行）与 `updateMessageById(assistantMessageId, ...)`（约 4383 行）
- `catch` 里的 `updateMessageById(assistantMessageId, ...)`（约 4399 行）、`messages.value.find((message) => message.id === assistantMessageId)`（约 4405 行）、`filter((message) => message.id !== assistantMessageId)`（约 4407 行）

**不要换**：起始的 `messages.value = [...]`（约 3998 行）、`slash_reply` 分支（约 4043 行）、`run_started` 分支（约 4065、4071 行）、`handleRunEvent(runEvent, assistantMessageId, ...)` 调用（约 4319 行，`handleRunEvent` 内部自己解析）。改完用 `grep -n "assistantMessageId" frontend/src/composables/useChatStream.ts` 逐行核对。

**9f.** `frontend/src/composables/useChatStream.test.ts`：

1. 在 `it('shows a tool card from live step events even with no plan running', ...)`（约 1894 行）之前追加：

```ts
  it('draws a steer the run handed its model as its own message between two answers', withFakeWebSocket(async () => {
    const { stream, socket, pendingSend } = await startLiveStream('live-delivered', 'run-delivered')

    runEvent(socket, 'evt-1', 1, 'run-delivered', 'live-delivered', 'assistant_delta', { text: 'checking the build' })
    runEvent(socket, 'evt-2', 2, 'run-delivered', 'live-delivered', 'input_delivered', { text: 'also run lint' })
    runEvent(socket, 'evt-3', 3, 'run-delivered', 'live-delivered', 'assistant_delta', { text: 'lint is clean' })

    expect(stream.messages.value.map((message) => [message.role, message.content])).toEqual([
      ['user', 'run the tests'],
      ['assistant', 'checking the build'],
      ['user', 'also run lint'],
      ['assistant', 'lint is clean'],
    ])
    expect(stream.messages.value.slice(1).map((message) => message.runId)).toEqual(['run-delivered', 'run-delivered', 'run-delivered'])

    endTurn(socket, 'evt-done', 4, 'run-delivered', 'live-delivered')
    await pendingSend
    // The run's end settles the answer it was drawing into, not the closed one.
    expect(stream.messages.value.map((message) => [message.role, message.content])).toEqual([
      ['user', 'run the tests'],
      ['assistant', 'checking the build'],
      ['user', 'also run lint'],
      ['assistant', 'lint is clean'],
    ])
  }))
```

2. 在 `it('reloads a failed run with its error inside the turn, not as a fresh alert', ...)`（约 866 行）之后追加：

```ts
  it('reloads a steer the run was handed once, from the transcript', async () => {
    const chatMessagesSpy = vi.spyOn(forebrainApi, 'chatMessages').mockResolvedValue([
      { id: 'u1', role: 'user', content: 'run the tests', runId: 'r-steer' },
      { id: 'a1', role: 'assistant', content: 'checking the build', runId: 'r-steer' },
      { id: 'u2', role: 'user', content: 'also run lint', runId: 'r-steer' },
      { id: 'a2', role: 'assistant', content: 'lint is clean', runId: 'r-steer' },
    ] as never)
    const sessionEventsSpy = vi.spyOn(forebrainApi, 'sessionEvents').mockResolvedValue({
      sessionId: 's-steer', nextCursor: 1, highWater: 1, hasMore: false, schemaVersion: 1,
      events: [
        { id: 'e1', sequence: 1, schemaVersion: 1, sessionId: 's-steer', runId: 'r-steer', type: 'input_delivered', createdAt: '2026-10-03T09:00:01Z', payload: { text: 'also run lint' } },
      ],
    } as never)
    const legacySpy = vi.spyOn(forebrainApi, 'sessionSubagentHistory').mockResolvedValue({ sessionId: 's-steer', records: [] })
    try {
      const stream = useChatStream()
      stream.sessionId.value = 's-steer'
      await stream.loadMessages('s-steer')

      expect(stream.messages.value.map((m) => m.role)).toEqual(['user', 'assistant', 'user', 'assistant'])
      expect(stream.messages.value.filter((m) => m.role === 'user' && m.content === 'also run lint')).toHaveLength(1)
    } finally {
      chatMessagesSpy.mockRestore()
      sessionEventsSpy.mockRestore()
      legacySpy.mockRestore()
    }
  })
```

**Verify**: `pnpm -C frontend exec vitest run src/composables/useChatStream.test.ts` → 全部 passed（含两个新用例）。

### Step 10: web 的 Shift+← 只要队列可见就撤回（G4）

**10a. `frontend/src/components/ForebrainPromptTextarea.vue`**：

1. props（36-43 行）在 `duringRun?: boolean` 之后加：

```ts
  /**
   * The composer's queue preview shows a message. Recalling the newest one
   * (Shift+← or Alt+↑) works whenever one is shown: a subagent's view keeps
   * its own queue while the conversation is idle, and a page reloaded mid-run
   * shows the run's queue without having started it.
   */
  queueVisible?: boolean
```

2. 第 597 行：

```ts
  if (props.duringRun && ((e.altKey && e.key === 'ArrowUp') || (e.shiftKey && e.key === 'ArrowLeft'))) {
```

改为：

```ts
  if ((props.duringRun || props.queueVisible) && ((e.altKey && e.key === 'ArrowUp') || (e.shiftKey && e.key === 'ArrowLeft'))) {
```

**10b. `frontend/src/views/ChatView.vue`**：在 `composerPendingInput` computed（943-946 行）之后加：

```ts
// Whether the composer's queue preview shows anything: recalling the newest
// message is offered exactly as long as one is shown.
const composerQueueVisible = computed(() => {
  const preview = composerPendingInput.value
  return preview.pendingSteers.length + preview.rejectedSteers.length + preview.queuedMessages.length > 0
})
```

并在 `<ForebrainPromptTextarea ...>`（507 行，`:during-run="isStreaming"` 那个）上加 `:queue-visible="composerQueueVisible"`。

**10c. `frontend/src/composables/useChatStream.ts`** 的 `editLastQueuedMessage`（3119-3133 行），把：

```ts
    const resp = await forebrainApi.runQueuedInput(runId, { action: 'edit_last' })
    applyPendingInputPreview(resp.preview)
    if (!resp.accepted) return null
    return queuedSubmission(resp)
```

改为：

```ts
    try {
      const resp = await forebrainApi.runQueuedInput(runId, { action: 'edit_last' })
      applyPendingInputPreview(resp.preview)
      if (!resp.accepted) return null
      return queuedSubmission(resp)
    } catch {
      // The run ended between the preview and the key: its queue is being
      // handed back as it ends, so there is nothing left to recall here.
      return null
    }
```

**10d. 测试**：

1. `frontend/src/components/ForebrainPromptTextarea.test.ts`：把 `mountComposer()` 改为接受 props——签名改成 `function mountComposer(props: Record<string, unknown> = {})`，slot 里的 `h(ForebrainPromptTextarea)` 改成 `h(ForebrainPromptTextarea, props)`；既有调用不传参数，行为不变。然后在 `it('reports the reader typing and an idle Escape', ...)` 之后追加：

```ts
  it('recalls the newest queued message on Shift+← whenever the queue shows one', async () => {
    const shown = mountComposer({ queueVisible: true })
    await shown.composer.find('textarea').trigger('keydown', { key: 'ArrowLeft', shiftKey: true })
    expect(shown.composer.emitted('edit-last-queued')).toHaveLength(1)

    const empty = mountComposer()
    await empty.composer.find('textarea').trigger('keydown', { key: 'ArrowLeft', shiftKey: true })
    expect(empty.composer.emitted('edit-last-queued')).toBeUndefined()
  })
```

2. `frontend/src/composables/useChatStream.test.ts`，在 `it('recalls the newest queued message whole, with its attachments described', ...)`（约 2736 行）之后追加：

```ts
  it('recalls nothing, and does not throw, when the run ended before the key', withFakeWebSocket(async () => {
    const queued = vi.spyOn(forebrainApi, 'runQueuedInput').mockRejectedValue(new Error('active run input not available'))
    try {
      const { stream, socket, pendingSend } = await startLiveStream('live-gone', 'run-gone')
      await expect(stream.editLastQueuedMessage()).resolves.toBeNull()
      endTurn(socket, 'evt-done', 1, 'run-gone', 'live-gone')
      await pendingSend
    } finally {
      queued.mockRestore()
    }
  }))
```

**Verify**:
- `pnpm -C frontend exec vitest run src/components/ForebrainPromptTextarea.test.ts src/composables/useChatStream.test.ts src/views/ChatView.test.ts` → 全部 passed
- `pnpm -C frontend test` → 全部 passed
- `git status --short frontend` → 只有 Scope 列出的前端文件；没有 `tsbuildinfo`、`dist`

### Step 11: 全量回归

**Verify**（全部必须满足）：
- `CGO_ENABLED=1 go test -tags fts5 ./pkg/run -count=1` → `ok`
- `CGO_ENABLED=1 go test -tags fts5 ./... -count=1 -timeout 20m` → 全部 `ok`
- `CGO_ENABLED=1 go test -race -tags fts5 ./pkg/run -run 'Retract|Steer|Recall|InputQueue|Compact|Overflow|Abandoned|Discard|Subagent' -count=1` → `ok`，无 `DATA RACE`
- `go vet ./pkg/run/... ./pkg/gateway/... ./pkg/event/...` → 无输出
- `gofmt -l cmd pkg third_party` → 无输出
- `pnpm -C frontend test` → 全部 passed
- `grep -n "unretractable steer is skipped" pkg/run/turn_input_test.go` → 无输出
- `git status --short -- . ':!plans'` → 只列出 Scope 里的文件（`plans/` 下的计划与索引文件不算）

### Step 12: TUI 真机验收（修复后）

（`Discard` 的真机路径是切换会话，由 Step 5 的引擎测试与 `pkg/tui` 既有的 `TestSessionSwitch*` 覆盖；web 端没有可自动驱动的真机环境，由 Step 6–10 的 gateway 与 vitest 测试覆盖，浏览器复核清单见「Maintenance notes」。这里只验 TUI 的 Shift+←。）

用 Step 1b 的同一脚本（先 `.claude/skills/run-forebrain/driver.sh build` 重建二进制——脚本第一行已包含），**Expected（修复后）**：

1. `requests before=N after=M` 且 `M > N`：按键后 2 秒内就发出了新请求（被中止的调用已去掉 steer 重发）。
2. 最新的请求 dump 不含 steer：`grep -c RECALL-ME-STEER "$(ls "$DUMP"/request-*.json | tail -1)"` → `0`；而 Step 1b 里找到的那份（被中止的）仍是 `1`。
3. `screen-after.txt`：`RECALL-ME-STEER` 出现在 composer 输入行，队列预览里不再列出它。
4. 再等 ~25 秒让 turn 结束（重发的请求同样被延迟 20 秒）：在脚本的 `$D stop` 之前插入 `sleep 25; $D messages`，其输出里没有内容为 `RECALL-ME-STEER` 的 user 行。

把修复前（Step 1b）和修复后两份 `screen-after.txt` 的关键行附在最终报告里。

### Step 13: 更新索引

把 `plans/README.md` 计划列表中本计划的状态从 `TODO` 改为 `DONE`。

## Test plan

- **runtime 层**（`pkg/run/turn_input_test.go`）：在途交付可撤回、撤回后 Commit 被拒、Rollback 只放回剩余、调用开始前撤回在 bind 时立即中止、已 Commit 不可撤回、seq 0 守卫。
- **队列层**：Q4 改写——在途 steer 现在可 `Recall`；模型已在回答的 steer 仍被跳过（用 `rt.SetChangeHook(nil)` 模拟 Commit 与队列收到通知之间的瞬间）。
- **编排层**（`pkg/run/orchestration_llm_test.go`）：工具边界后撤回 → 中止 + 去掉重发；最终回答处重开后全部撤回 → 以原回答结束、usage 只计一次；首个输出事件后撤回失败；撤回与首个输出事件竞速时，被丢弃调用的输出不漏到 sink 和 session。
- **清空队列**：`Discard` 撤走在途 steer 并计数、模型已在回答的不计数、detach 后全部计数（`TestInputQueueDiscardCountsWhatItDrops` 改写）；编排层切换会话式丢弃 → 调用中止、不带 steer 重发、steer 不进 session 也不渲染（`TestDiscardRetractsTheInFlightSteer`）。
- **gateway**：在途 steer 经 `edit_last` 拿回且承载它的调用不 Commit（`TestHandleRunQueuedInputEditLastRetractsInFlightSteer`）；送达的 steer 发且只发一次 `input_delivered`、排在不再列出它的预览之前、delivered 列表被取空（`TestRunQueuePublishesDeliveredSteerOnce`）；subagent surface 把队列变化发成带 `agent_id` 的预览（`TestSubagentSurfacePublishesQueueChanges`）。
- **subagent 引擎**：入队、送达、撤回、执行结束结算都推给 surface（`TestSubagentQueueChangesReachTheSurface`）。
- **web**（vitest）：实时把送达的 steer 拆成独立气泡、run 结束收尾落到最后一个回答；刷新时以 transcript 为准、送达事件不重复；Shift+← 在队列可见时撤回、队列空时不劫持；run 刚结束时撤回返回 null 不抛错。范本：`useChatStream.test.ts` 的 `startLiveStream`/`runEvent`/`endTurn`（1856-1892 行）与 `reloads a failed run ...`（866 行）。
- **压缩交互**（`compaction_test.go` + `orchestration_llm_test.go`）：主动压缩不吞在途 steer；压缩期间撤回不取消压缩、checkpoint 被保留且不重做、重发不带 steer；被动（溢出）压缩同样不吞暂定消息。
- 结构范本：`TestFailedSamplingReturnsDrainedSteerToQueue`（`orchestration_llm_test.go:4129`）、`TestOrchestrationAutoCompactsDuringTurnWithoutSnapshot`（`compaction_test.go:44`）、`TestStreamingOverflowRecoversAndContinues`（`orchestration_llm_test.go:111` 附近）。
- 既有测试必须原样通过，尤其：`TestSteerDeliveryCommitsBeforeFirstStreamEvent`、`TestSteerDeliveryStaysCommittedWhenStreamFailsAfterResponseStart`、`TestFailedSamplingReturnsDrainedSteerToQueue`、`TestFailedSamplingReturnsInjectedSteerToQueue`、`TestCommittedSteerIsNotReturnedByALaterFailure`、`TestCancelledRunKeepsQueuedSteer*`、`TestOrchestrationAdoptsCompactedSession`、`TestInputQueueDiscardCountsWhatItDrops`、`TestRecallSteerAfterRuntimeDetach`。

## Done criteria

- [ ] Step 1b 记录了修复前证据（按键后无新请求、composer 为空）
- [ ] `CGO_ENABLED=1 go test -tags fts5 ./... -count=1 -timeout 20m` 全部通过
- [ ] `CGO_ENABLED=1 go test -race -tags fts5 ./pkg/run -run 'Retract|Steer|Recall|InputQueue|Compact|Overflow|Abandoned' -count=1` 通过且无 `DATA RACE`
- [ ] Step 4 列出的 8 个新测试 / 子测试、Step 5 的 `TestDiscardRetractsTheInFlightSteer` 全部存在且 PASS；`TestInputQueueDiscardCountsWhatItDrops` 含 q2/q3/q4 三段新断言
- [ ] `grep -n "RetractLastSteer" pkg/run/turn_input.go` 只命中 `RetractLastSteer` 自身的 doc 注释与定义（`Recall`、`Discard` 都已改用 `RetractSteer`）
- [ ] `go vet ./pkg/run/...`、`gofmt -l cmd pkg third_party` 均无输出
- [ ] `grep -n "unretractable steer is skipped" pkg/run/turn_input_test.go` 无输出
- [ ] `grep -rn "restoreSteers" pkg` 无输出；`grep -A7 "^type SteerDelivery struct" pkg/run/turn_input.go | grep -c "sync.Mutex"` 输出 `0`（`SteerDelivery` 已无自有锁）
- [ ] Step 6–10 的新测试全部存在且通过：`TestHandleRunQueuedInputEditLastRetractsInFlightSteer`、`TestSubagentQueueChangesReachTheSurface`、`TestSubagentSurfacePublishesQueueChanges`、`TestRunQueuePublishesDeliveredSteerOnce`，以及 vitest 用例 `draws a steer the run handed its model as its own message between two answers`、`reloads a steer the run was handed once, from the transcript`、`recalls the newest queued message on Shift+← whenever the queue shows one`、`recalls nothing, and does not throw, when the run ended before the key`
- [ ] `grep -n "SetChangeHook" pkg/gateway/*.go` 只命中 `watchRunQueue` 一处
- [ ] `pnpm -C frontend test` 全部 passed
- [ ] `git status --short -- . ':!plans'` 只有 Scope 内文件被改（特别是没有 `frontend/tsconfig*.tsbuildinfo`）
- [ ] Step 12 真机验收 4 条全部满足，修复前后 screen 已附报告
- [ ] `plans/README.md` 状态行为 DONE

## STOP conditions

满足任一条即停止并回报，不要即兴发挥：

- Drift check 显示 in-scope 文件自 `71c0449` 后有改动，且「Current state」摘录与实码不一致。
- Step 1b 复现失败：steer 从未出现在任何请求 dump 里，或修复前按 Shift+← 就已经能撤回——说明验收脚本没打到在途窗口（例如标题生成等额外请求打乱了假 provider 的脚本顺序），先报告 `provider-log` 与 screen，不要改代码去迁就。
- 任何既有测试因本改动失败——特别是 `TestSteerDelivery*`、`TestFailedSampling*`、`TestCommittedSteer*`、`TestOrchestrationAdoptsCompactedSession`、任何 `TestCharacterization*`（`pkg/tui`）。这些测试编码的是交付与压缩采纳的既有契约。
- 某个既有测试以 `context canceled` 失败：说明有代码在 `w.inner.Execute` 返回后仍在用那次调用的 ctx（本计划在调用返回后立即 `abandonCall(nil)`）——报告是哪条路径，不要去掉 `abandonCall(nil)`。
- `-race` 报 `DATA RACE`，且涉及 `SteerDelivery`/`TurnInputRuntime`/`InputQueue` 以外的类型。
- 发现 `recoverableLLM` 之外还有地方调用 `recordCompactionAdoption`（`grep -n "recordCompactionAdoption(" pkg/run/*.go` 应只命中 `compaction.go` 定义处与 `llm_middleware.go` 两处）——撤回重发路径「adoption 一定以 steer 结尾」的前提会失效。
- `pkg/tui` 的 `TestSessionSwitch*` 或 `pkg/run` 的 subagent 丢弃测试（`DiscardSubagentInput` 的断言）因 `Discard` 计数变化失败——说明有调用方依赖旧计数，报告具体断言，不要改测试迁就。
- Step 7 的 `-race` 或测试超时/卡死：说明 `OnQueueChanged` 在持有通道锁时回调进了通道（死锁）或与通道状态竞争——报告调用栈，不要去掉 `queueHookMu`，也不要把 `withdraw` 里的 `TakeAll` 挪出锁外。
- Step 7 改动前的 `grep -rn "SetChangeHook("` 发现有代码给 subagent 的 worker 会话队列装 hook（会与通道的 hook 互相覆盖）。
- `TestSubagentSurfacePublishesQueueChanges` 查不到事件（会话级事件没有落库，例如 `ErrSessionNotStarted`）——报告 `newRunInputTestServer` 建会话的方式，不要改 gateway 的事件总线。
- Step 9 的实时拆分用例里消息结构与预期不同（例如 `turn_started` 额外造了一条消息，或某个 helper 在 run 开始时就记住了 `assistantMessageId`，导致后续输出仍落在被收尾的气泡上）——报告实际的 `messages` 结构与相关代码位置，不要改用例迁就。
- 任何既有的 vitest 用例因本改动失败。
- 修复看起来必须改 out-of-scope 文件（`pkg/tui/**`、`pkg/assembly/**`、`PendingInputQueuePopover.vue`）。

## Maintenance notes

- **不变式**：steer 任何时刻恰好在 runtime 的 `pending` 或某个未结算 `SteerDelivery` 里（同一把 `rt.mu`）；只要它还在 `q.steers`（队列预览可见），`RetractSteer` 就能拿到它，除非该交付已 Commit（此时 hook 正在把它移出预览）。今后任何「从 pending 取走 steer」的新路径都必须走 `BeginSteerDelivery`（登记 inflight），否则会重新出现「看得见撤不回」。
- **Commit 与撤回的互斥**是正确性的核心：编排循环里「成功后先 Commit、再查 `Retracted()`」的顺序不能调换；`withSteerDeliveryResponseStart` 里「Commit 后查 `Retracted()`」同理。审阅时重点看这两处。
- **暂定尾巴**：编排循环用 `withProvisionalTail` 告诉 `recoverableLLM` 末尾多少条消息未定。以后如果在 orchestration 与 `recoverableLLM` 之间加会改写消息切片的包装（目前 guardrails 只读不改），`provisionalTailLen` 的长度校验会让它退化为「整体压缩」——不会出错但会丢失本计划的保证，需要同步传递尾巴长度。
- **压缩 ctx**：`compactionContext` 只在有暂定尾巴时把压缩从调用 ctx 上摘下来，挂到编排 ctx 的取消上；Esc 取消 run 仍会取消压缩。
- **被丢弃调用的代价**：每次撤回在途 steer 会放弃一次已发出的 provider 请求（输入 token 可能已计费），并让 reminder 包装在重发时重新注入（`reminderSink.discard()`，与「失败调用不采纳 reminder」的既有语义一致；LSP 诊断提醒用 Peek/Ack，未 Ack 会重注入）。
- **gateway / subagent**：两者都经 `InputQueue.Recall` 自动获得新语义；web 的 `edit_last` 现在也能拿回在途 steer。
- **web 的队列状态只来自事件**：主会话靠 `watchRunQueue` 的 hook，subagent 靠 `OnQueueChanged`。今后新增任何会改动队列、又不经 `InputQueue` 方法的路径，web 都看不到——队列的所有变化必须走 `InputQueue` 的方法（它们都会触发 change hook）。hook 在不同 goroutine 上触发，读的是触发时的预览，极端情况下旧预览可能晚于新预览到达；HTTP 处理函数在操作后立刻回的预览会纠正它（既有模式，本计划未改变）。
- **web 的 run 投影可以中途换回答气泡**：`answerTarget(state, 起始 id)` 是唯一的取目标入口。今后在 `handleRunEvent` 或 `send()` 收尾处新增「写回答消息」的代码，必须通过它取 id；直接用 `assistantMessageId` 会把输出写回已收尾的气泡。
- **web 真机复核清单（owner 手动，计划内无法自动化）**：① 主会话：工具运行时发一条 steer，在下一次请求首 token 前按 Shift+← → 回到输入框、不再出现在对话里；② 主会话：让 steer 正常送达 → 它作为用户气泡出现在两段回答之间，刷新后形态不变；③ subagent 视图（主会话空闲）：给运行中的 subagent 发 steer，送达后队列浮层不再列出它；队列里还有消息时按 Shift+← 能撤回。
- **所有「把 steer 拿出队列」的路径都必须经 `RetractSteer`**：`Recall`、`Discard` 已改。`TakeAll`/`Next` 仍用 `consumeSteersLocked`（只取 `pending`），这依赖它们的调用时机不会遇到在途交付——若将来在 run 进行中（工具边界之后）调用它们，必须一并改成按 `Seq` 撤回。
- `RetractLastSteer` 已无生产调用方，只剩测试；新代码不要用它撤回 steer（它看不到在途交付，会重新引入本 bug）。
