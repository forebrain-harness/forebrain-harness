# Plan 142：子代理视图只在子代理运行中可以插话（引擎 + 网关 + 终端）

> **执行者须知**：按步骤顺序执行，每步跑验证命令并确认预期结果后再进下一步。触发
> "STOP conditions" 时停下报告，不要即兴发挥。完成后把 `plans/README.md` 里本计划的状态行改为 DONE
> （若评审方派发你并说明由其维护索引，则不改）。
> **禁止提交代码**（owner 手动提交）：不 commit、不 push、不开 PR、不建分支，改动直接留在工作区。
> 工作区里与本计划无关的未提交改动**不要碰、不要回滚、不要格式化**。
>
> **前置 1（否则 STOP 1）**：`plans/README.md` 里 `140`、`141` 都是 DONE；且
> `grep -n "func subagentAwaitedFromContext" pkg/run/subagent.go`、`grep -n "func (w \*toolOrchestrationLLM) steerMessages" pkg/run/orchestration_llm.go` 各 1 条。
>
> **前置 2（否则 STOP 1）——目标文件必须干净**：
> `git status --porcelain pkg/run pkg/process pkg/turn/slash.go pkg/turn/slash_test.go pkg/turn/model.go pkg/tui pkg/gateway .claude/skills/run-forebrain docs/plan/SUBAGENT_CONVERSATION/README.md`
> 必须**无输出**（140、141 的改动须已由 owner 提交）。
>
> **Drift check**：本计划以 140、141 落地后的代码为准，下文"现状"按**函数名**描述；行号是 `82ab168` 时的近似值，只作定位参考。
>
> **基线（Step 1 之前跑一次，记下失败清单；否则 STOP 5）**：
> `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/run ./pkg/turn ./pkg/process ./pkg/tui ./pkg/gateway -count=1`（超时给足 600 s）。

## Status

- **Priority**: P1（owner 2026-10-26 指定）
- **Effort**: L
- **Risk**: HIGH（重写 `runSubagentExecution`；删除撤回、子代理 `/compact`、用户空闲启动三条现有能力；约 20 个测试迁移）
- **Depends on**: `140`（`AwaitedByDispatcher`、`withSubagentAwaited`）、`141`（插话携带技能、`continueForkSubagent` 加载技能）
- **Blocks**: `143`（网页对齐）
- **Category**: direction（推翻 SUBAGENT_CONVERSATION 的 D2、D3 前提，修改 D1、D4）
- **Planned at**: dev `82ab168`，2026-10-26

## owner 的要求与裁定（2026-10-26）

原话：

> 用户直接在子代理视图跟子代理直接发起对话这种情况下，这个子代理实质也是主代理派发的，用户只可能在这个子代理正在执行主代理派发的任务时与子代理对话，
> 纠正子代理的行为，子代理完成任务后仍然会将工具结果交回主代理。应该加一个限制，子代理完成任务并将工具结果交回主代理后，用户就无法再跟这个子代理进行对话，
> 该子代理视图应该隐藏composer，使用户无法再向该子代理发送消息

裁定（逐条，均已确认）：

1. **口径**：子代理有执行在跑（含等审批）时显示输入框、可以插话；空闲即隐藏。不分派发方式（`subagent_send` 后台子代理、计划审阅、/goal 检查同样适用）。**引擎也拒绝**用户向空闲子代理发消息（网关 API 同样拒绝）。
2. **未送达的消息**：执行结束时队列里还没送达的消息（及空闲后残留的草稿）**退回主视图输入框**，并在子代理视图提示一句。不自动发给任何代理。
3. **"中断并立即发送"（D1 第二义）**：中断当前这一步后，插话**在同一次执行里接着跑**（不再新开一次执行）——等待它的 `subagent_run` / `subagent_fanout` / `subagent_continue` 仍拿到最终结果，卡片还是那一张。
4. **撤回（D1 第一义）删除**：它只对"用户发起的执行"生效，新规则下这类执行不再存在。
5. **"排到之后"（follow-up）删除**：子代理视图里一律按插话处理。
6. **子代理视图的 `/compact` 删除**（终端与网关端点一并删）；`/context` 保留（运行中可用）。
7. 技能命令：作为插话送达并真实激活（141 已实现，本计划只保证路径还通）。
8. 网页对齐另立 `143`，免原型。

## Why this matters

今天用户可以在**空闲**子代理的视图里发消息，引擎会为它新开一次"用户发起的执行"（`run.SendToSubagent` → `startUserSubagentExecution`）。这次执行的结果**不回主代理**
（D3），主代理对它一无所知；它还会和主代理随后的 `subagent_continue` 抢同一个子代理。owner 的定位：子代理是主代理派的，用户的话只用于在它干活时纠偏；
活干完、结果交回主代理以后，后续由主代理决定（140 已让失败的子代理由主代理用 `subagent_continue` 续跑）。

## Current state（140、141 落地后）

### 引擎：`pkg/run/subagent.go`

- `SendToSubagent(ctx, r, surface, conversationSessionID, agentKey, in, mode)`（约 `:2025-2047`）：
  ```go
  ch, record, err := subagentChannelForConversation(r, conversationSessionID, agentKey)
  …refuseGoneCustomSubagent…
  ch.setSurface(surface)
  ch.mu.Lock(); running := ch.running; ch.mu.Unlock()
  if running != nil {
      if mode == TurnInputModeSteer && ch.queue.Steer(in) {
          return SubagentDeliverySteered, nil
      }
      ch.queue.FollowUp(in)
      return SubagentDeliveryQueued, nil
  }
  fac := r.subagentFactory()
  go startUserSubagentExecution(ctx, fac, ch, record, in)
  return SubagentDeliveryStarted, nil
  ```
  调用方：`pkg/tui/chat_session.go` `(*ChatSession).SendToSubagent`、`pkg/gateway/api_extra.go` `handleSubagentInput`、`pkg/process/one_shot.go` `ContinueSubagent`（自动续跑）。
- `startUserSubagentExecution(ctx, fac, ch, record, in)`（约 `:2142-2265`）：建 `lifecycle`（`ParentToolCallID = ""`、`TaskIndex = 0`、`Task = text`）、登记 registry handle、
  `notifySubagentSpawnedDirect(fac, lifecycle, "user")`、按 `record.AgentKind` 选 fork（`continueForkSubagent`）或 typed（`executeSubagent(..., text, in.Parts, ...)`），
  `runSubagentExecution(hostCtx, fac, lifecycle, true, in, …)`，之后有一大段 `if errors.Is(runErr, errSubagentInputWithdrawn) { … }` 的撤回收尾，再写终态与 ended 事件。
- `runSubagentExecution(ctx, fac, record, byUser bool, input Input, run)`（约 `:848-901`，140 已加 `awaited := subagentAwaitedFromContext(ctx)` 与 `reportSubagentExecutionEnded(…, awaited)`）：
  一个 `execCtx`、一个 `subagentExecution{executionID, byUser, cancel, input}`、`rt := NewTurnInputRuntime(); ch.queue.Attach(rt)`、`withSubagentResponseStarted`、`withSubagentUserTurn`、
  `rt.AddChangeHook(publishInputDelivered)`、`ch.setRunning(ex)`、跑 `run`、`ch.queue.Detach()`、清 `ch.running`、撤回分支、
  `send, restore := ch.queue.Next(boundary)`、`surface.OnBoundary(ch.agentKey, send, restore)`。
- `subagentExecution`（约 `:640-656`）字段：`executionID`、`byUser`、`responded`、`withdrawn`、`cancel`、`boundary`、`input`、`userRowID`。
- 撤回相关：`(*subagentChannel).withdraw`（约 `:811-841`）、`WithdrawSubagentInput`（约 `:2082-2092`）、`errSubagentInputWithdrawn`（约 `:571-579`）、
  `markResponded` / `setUserRow`（约 `:740-760`）、`withSubagentResponseStarted` / `subagentResponseStartedFromContext`、`withSubagentUserTurn` / `subagentUserTurnFromContext`（约 `:498-536`）、
  `subagentRunContext` 里 `EventStreamSink(..., subagentResponseStartedFromContext(ctx))`（约 `:976`）、`executeSubagent` 里 `OnUserTurn: subagentUserTurnFromContext(ctx)`（约 `:571`）、
  `SubagentExecRequest.OnUserTurn`（约 `:3441-3445`）、`pkg/process/worker_cli.go` 约 `:110-114` 调 `req.OnUserTurn(rowID)`。
  **`state.SessionStore.WithdrawUserTurn` 主对话也在用，不能删。**
- 中断并发送：`(*subagentChannel).interruptToSend`（约 `:795-809`）把 `ex.boundary = BoundaryInterruptToSend` 并 `cancel(errSubagentInterruptedToSend)`；`InterruptSubagentToSend`（约 `:2074-2080`）。
- `SubagentSurface`（约 `:586-605`）：`Frame`、`OnBoundary func(agentKey string, send, restore []Input)`、`OnQueueChanged`。
- 子代理 `/compact`：`SubagentCompactTarget`（约 `:1934-1953`），`ErrSubagentRunning`（约 `:1925-1927`）。
- `InputQueue.Next(b Boundary)`（`pkg/run/turn_input.go` 约 `:750-805`）：`BoundaryInterruptToSend` 释放 `steers` 为 send；`BoundaryInterrupted` 把 rejected + steers + followUp 全部作为 restore 退回。

### 终端

- `Session` 接口（`pkg/tui/notify.go` 约 `:1840-1875`）：`SendToSubagent(sessionID, agentKey string, submission ComposerSubmission, followUp bool)`、`InterruptSubagentToSend`、
  `WithdrawSubagentInput`、`DiscardSubagentInput`、`CompactSubagent`、`SubagentContextReport` 等。实现在 `pkg/tui/chat_session.go`（约 `:1526-1615`）；测试替身 `fakeSession` 在 `pkg/tui/run_test.go`。
- `pkg/tui/chat_session.go` `subagentSurface`（约 `:1660-1685`）把 `OnBoundary` 转成 `SubagentInputBoundaryMsg{AgentKey, Send, Restore}`（定义在 `notify.go` 约 `:474-480`）；
  `pkg/tui/run.go` `handleSubagentInputBoundaryMsg`（约 `:2598-2625`）：Send 再发给子代理，Restore 回子代理视图的输入框。
- `pkg/tui/run.go`：`subagentViewLine`、`subagentViewSlash`（`compact` → `dispatchSubagentCompactSlash`）、`sendSubagentViewSubmission(…, followUp bool)`、
  `handleSubagentViewEsc`（撤回 → 中断并发送 → 取消自动续跑 → 返回主视图）、两处 `hotkeyQueueFollowUp` 在子代理视图里调 `sendSubagentViewSubmission(…, true)`（约 `:4648-4660`、`:4926-4934`）。
- 输入框渲染：`renderComposerWithState`（`run.go` 约 `:3276-3335`）→ `Renderer.RenderComposerState(ComposerRenderState{…})`（`render.go` 约 `:3806`）→ `paintViewportLocked` → `buildComposerBlock`（`reducer.go` 约 `:3554-3570`）
  → `buildNormalComposerBlock`（约 `:3642-3721`：状态行、预览、边框 + 输入行 + 边框、footer、roster）。`composerBlock{lines, cursorRow, cursorCol, text}`，`text` 零值合法（"Zero for overlay blocks"）。
  子代理视图的 footer 由 `subagentFooterLeftLocked` 给出（"viewing <type> · <id> · esc to return · <model>"）。
- 原始读键器的闸门先例：`pkg/tui/input_events.go` 的 `uiPanelActive`（约 `:2503-2508`），在 `:1399` 处吞掉除 Esc（0x1b）、Ctrl+C（0x03）、Tab 外的所有字节；
  方向键在 `:1250` 处被 `slashOverlayActive || mentionOverlayActive || agentRosterFocused || uiPanelActive` 改成 `hotkeyOverlayUp/Down`。
- 自动续跑提示：`pkg/tui/notify.go` `autoContinueTranscriptText(code, at, now)`（约 `:2447`）结尾 `" · esc or type to cancel"`，`announceAutoContinue`（约 `:2336`）按视图写进 transcript。

### 网关

- `pkg/gateway/api_extra.go`：路由（约 `:218-224`）`…/subagents/:agent/input`、`queued-input`、`interrupt-send`、`withdraw`、`compact`、`context`、`budget`；
  `handleSubagentInput`（约 `:2840-2880`，读 `req.Mode` 决定 steer / follow_up；错误除 `ErrSubagentNotFound` 外一律 400）；`subagentCommandInput`（`case "compact", "context": return useDedicatedEndpointCode, false`）；
  `handleSubagentWithdraw`（约 `:2976-3000`）；`handleSubagentCompact`（约 `:3002-3036`）；错误码常量（约 `:2690-2694`）`subagentRunningCode`、`subagentViewCommandCode`、`useDedicatedEndpointCode`；
  `subagentConversationSurface`（约 `:2753-2770`）的 `OnBoundary` → `publishSubagentQueuedInputReleased(sid, agentKey, send, restore)`（`payload.Inputs` = restore，`payload.Next` = send）。

### turn

- `pkg/turn/slash.go:86` `compact` 的 `SubagentView: SubagentViewActs`；`pkg/turn/model.go` 约 `:160-162` 注释 "(/compact, /context)"；`pkg/turn/slash_test.go` 约 `:390` 期望表。

### 工具与文档

- `.claude/skills/run-forebrain/fake_provider.py` 的 `subagent-net` 模式（"子代理断网失败后，用户在它的视图里发 continue"）——该流程本计划之后不存在；`SKILL.md` 表格与 `driver.sh` 帮助里各有一段说明。
- `docs/plan/SUBAGENT_CONVERSATION/README.md` 决策表（约 `:112-130`）的 D1、D2、D3、D4。

## Commands you will need

| 用途 | 命令 | 成功时 |
|---|---|---|
| 定向测试 | `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./<pkg> -run '^<名字>$' -count=1 -v` | 看到 `--- PASS`（**必须看到 PASS 行**） |
| 回归 | `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/run ./pkg/turn ./pkg/process ./pkg/tui ./pkg/gateway -count=1` | 与基线相比无新增 FAIL |
| 编译 / vet / gofmt | `CGO_ENABLED=1 go build -tags fts5 ./...`；`CGO_ENABLED=1 go vet -tags fts5 ./pkg/...`；`gofmt -l pkg` | exit 0 / exit 0 / 无输出 |
| 死代码 | `grep -rn 'WithdrawSubagentInput\|errSubagentInputWithdrawn\|OnUserTurn\|withSubagentUserTurn\|withSubagentResponseStarted\|SubagentCompactTarget\|CompactSubagent\|handleSubagentWithdraw\|handleSubagentCompact\|startUserSubagentExecution\|SubagentInputBoundaryMsg' pkg` | 无输出 |

## Scope

**In scope**：`pkg/run/subagent.go`、`pkg/run/subagent_test.go`、`pkg/run/turn_input.go`（只在需要时）、`pkg/process/one_shot.go`、`pkg/process/worker_cli.go`、
`pkg/turn/slash.go`、`pkg/turn/model.go`、`pkg/turn/slash_test.go`、`pkg/tui/`（`notify.go`、`chat_session.go`、`run.go`、`render.go`、`reducer.go`、`input_events.go` 及其测试）、
`pkg/gateway/api_extra.go`、`pkg/gateway/api_extra_test.go`、`pkg/gateway/server.go`（若路由登记在那里）、`.claude/skills/run-forebrain/{fake_provider.py,SKILL.md,driver.sh}`、`docs/plan/SUBAGENT_CONVERSATION/README.md`。

**Out of scope**：网页（`frontend/`，见 143；本计划改了网关行为，网页在 143 之前会出现"空闲时发送被 409 拒绝"——这是预期的过渡态）；
`state.SessionStore.WithdrawUserTurn`（主对话在用）；主对话的撤回、follow-up、`/compact`；计划审阅与 /goal 检查的派发逻辑；`subagent_*` 工具的结果形状。

## Steps

### Step 1：先写新语义的引擎测试（会红）

在 `pkg/run/subagent_test.go` 新增（搭建照 `TestInterruptSubagentToSendRunsThePendingSteersNext`：`newPersistenceFixture`、`fix.seedWorker`、`seedChannelRecord`、`channelTestExecutor{store, hold, release, answer}`、`waitSubagentBusy`、`waitProbeEvent`）：

1. `TestUserCannotStartAnIdleSubagent`：种一条空闲记录，`SendToSubagent(ctx, owner, SubagentSurface{}, conv, key, Input{Text: "continue"})` → `errors.Is(err, ErrSubagentNotRunning)`；
   执行器从没被调用（`channelTestExecutor` 的计数/`started` 通道为空）；对 one-shot 类型、保留类型（`plan-reviewer`）、fork 三种记录各断言一次（取代 `TestUserCanTalkToEveryKindOfSubagent`）。
2. `TestInterruptToSendContinuesInTheSameExecution`：用 `StartSubagentAutoContinue` 让子代理跑起来（`hold: true`），插两条话 `one`、`two`，`InterruptSubagentToSend` → true；
   释放执行器后：`subagent_spawned` 与 `subagent_ended` 事件**各只有 1 个**（同一个 `execution_id`）；执行器第二次被调用时收到的任务文本是 `one\n\ntwo`；
   `OnReturned` 从未被调用；`SubagentRunning` 最终为 false。
3. `TestExecutionEndHandsUnsentMessagesBack`：跑起来后插一条话，**不中断**，让执行器在送达之前结束（`channelTestExecutor` 的写法：它不经过工具边界就返回）；
   断言 `OnReturned(key, inputs)` 收到恰好那一条，且没有新执行启动（spawned 只 1 个）。
4. `TestSubagentViewOnlySteers`：运行中用（新签名的）`SendToSubagent` 发一条 → `SubagentDeliverySteered`。

新签名先在测试里按下文 Step 2 的形状写；此时编译失败也算"红"。

**Verify**：`CGO_ENABLED=1 go vet -tags fts5 ./pkg/run` 报未定义/签名不符，或测试 `--- FAIL`。

### Step 2：引擎——发消息、自动续跑、一次执行多段尝试

1. 新增错误与自动续跑入口：
   ```go
   // ErrSubagentNotRunning refuses a message to a subagent that is not running.
   // An idle subagent takes no messages: the agent that dispatched it decides
   // what it does next (owner, 2026-10-26).
   var ErrSubagentNotRunning = errors.New("subagent is not running")
   ```
   `SendToSubagent` 去掉 `mode` 参数，空闲返回 `ErrSubagentNotRunning`，运行中只走插话：
   ```go
   	if running == nil {
   		return "", ErrSubagentNotRunning
   	}
   	// A subagent's view only steers: an execution ends with nothing queued
   	// behind it, so there is no "after" to queue a message for.
   	if ch.queue.Steer(in) {
   		return SubagentDeliverySteered, nil
   	}
   	// No runtime is attached between two attempts of an execution: the queue
   	// keeps the message, and the execution's end hands it back if no attempt
   	// took it.
   	return SubagentDeliveryQueued, nil
   ```
   新增 `StartSubagentAutoContinue(ctx, r, surface, conversationSessionID, agentKey string, in Input) error`：查找、`refuseGoneCustomSubagent`、`setSurface`、运行中返回 `ErrSubagentRunning`、
   否则 `go startSubagentContinuation(ctx, fac, ch, record, in)` 返回 nil。注释写明：这是唯一一种"既不是工具调用、也不是用户消息"发起的执行（额度恢复后的自动续跑）。
   `pkg/process/one_shot.go` `ContinueSubagent` 改调它：出错即 `return turn.ErrAutoContinueUnavailable`。
2. `startUserSubagentExecution` 改名 `startSubagentContinuation`，删掉撤回收尾那整段 `if errors.Is(runErr, errSubagentInputWithdrawn) { … }`；
   fork/typed 的闭包改用下面的 `subagentResumeAttempt(fac, record, in, skills)`（`skills` 由 `in.SkillName/SkillPath` 组成，空则 nil），不再自己拼。其余（lifecycle、handle、spawned 用 `"user"` 来源、终态、ended）不变。
3. 新增三个小函数（放在 `runSubagentExecution` 附近）：
   ```go
   // steerEntries is how steers released by an interrupt are reported
   // delivered: the entries the queue would have handed the runtime at a tool
   // boundary, skill selection included.
   func steerEntries(steers []Input) []TurnInputEntry

   // mergeSteers is the one message an interrupt resumes the subagent with —
   // the steers in the order the user wrote them, joined by a blank line — and
   // every skill they selected.
   func mergeSteers(steers []Input) (Input, []SkillSelection)

   // subagentResumeAttempt runs a subagent once more on its own conversation,
   // starting from in: a fork on its frozen system and worker transcript, a
   // typed subagent through the executor with its worker session. skills are
   // the explicit skills the message selected; both paths load them (plan 141).
   func subagentResumeAttempt(fac Factory, record agent.HistoryEntry, in Input, skills []SkillSelection) func(context.Context) (string, error)
   ```
   `subagentResumeAttempt` 的主体就是今天 `startUserSubagentExecution` 里那段：`approve := tool.SubagentApprovalHookFromContext(execCtx, factoryToolsState(fac))`、
   `runAcrossApprovals(execCtx, approve, record.RunID, "subagent", …)`，fork（`record.AgentKind == "fork"`）调 `continueForkSubagent(attemptCtx, fac, record, text)`，
   否则 `executeSubagent(attemptCtx, fac, record, text, in.Parts, superviseRunID, record.ParentRunID, record.SessionID, record.WorkerSessionID, subagentType)`；`skills` 非空时先 `execCtx = WithExplicitSkillSelections(execCtx, skills)`。
4. 新增 `(*subagentChannel).beginAttempt(ctx, fac, ex) (context.Context, context.CancelCauseFunc)`：`context.WithCancelCause(ctx)`、新 `TurnInputRuntime`、`ch.queue.Attach(rt)`、
   `rt.AddChangeHook(func(d []TurnInputEntry) { ch.publishInputDelivered(fac, ex, d) })`、在 `ch.mu` 下设 `ex.cancel = cancel`，返回 `WithTurnInputRuntime(execCtx, rt), cancel`。
5. 重写 `runSubagentExecution`（签名去掉 `byUser`、`input`；6 个调用点同步改）：

   ```go
   // runSubagentExecution runs one execution of a subagent through its channel:
   // it waits for the subagent to be free, attaches the channel's steer runtime
   // while the execution runs, and settles the channel's queue at the end.
   //
   // An execution is one or more attempts. The user's interrupt-to-send stops
   // the running attempt, and the steers queued behind it start the next one on
   // the subagent's own conversation inside this same execution, so whoever
   // awaits it — the dispatching tool call above all — still gets its answer
   // (owner, 2026-10-26). When the execution ends, every message the user sent
   // that the subagent never took goes back to the user: the subagent is idle
   // then, and an idle subagent takes no messages.
   func runSubagentExecution(ctx context.Context, fac Factory, record agent.HistoryEntry,
   	run func(execCtx context.Context) (string, error)) (string, error) {
   	awaited := subagentAwaitedFromContext(ctx)
   	ch := subagentChannelFor(fac, record)
   	select {
   	case ch.exec <- struct{}{}:
   		defer func() { <-ch.exec }()
   	case <-ctx.Done():
   		return "", ctx.Err()
   	}
   	executionID := strings.TrimSpace(record.ExecutionID)
   	if executionID == "" {
   		executionID = uuid.NewString()
   	}
   	ex := &subagentExecution{executionID: executionID}
   	attempt := run
   	started := false
   	for {
   		execCtx, cancel := ch.beginAttempt(ctx, fac, ex)
   		if !started {
   			started = true
   			ch.setRunning(ex)
   			// The execution has taken the slot: any continuation the subagent
   			// was waiting on is superseded, because the conversation moved on.
   			reportSubagentExecutionStarting(fac, record)
   		}
   		out, err := attempt(execCtx)
   		cancel(nil)
   		ch.queue.Detach()
   		ch.mu.Lock()
   		boundary := ex.boundary
   		ex.boundary = BoundaryCompleted
   		surface := ch.surface
   		ch.mu.Unlock()
   		if boundary == BoundaryInterruptToSend && ch.store != nil {
   			if steers, _ := ch.queue.Next(BoundaryInterruptToSend); len(steers) > 0 {
   				ch.publishInputDelivered(fac, ex, steerEntries(steers))
   				in, skills := mergeSteers(steers)
   				attempt = subagentResumeAttempt(fac, record, in, skills)
   				continue
   			}
   		}
   		ch.mu.Lock()
   		if ch.running == ex {
   			ch.running = nil
   		}
   		ch.mu.Unlock()
   		if _, returned := ch.queue.Next(BoundaryInterrupted); len(returned) > 0 && surface.OnReturned != nil {
   			surface.OnReturned(ch.agentKey, returned)
   		}
   		reportSubagentExecutionEnded(fac, record, err, awaited)
   		return out, err
   	}
   }
   ```
   `ch.store == nil`（无状态库的测试装配）时不续跑，插话随执行结束退回——这是有意的：没有 worker session 就没有可续的对话。
6. `SubagentSurface.OnBoundary` 换成：
   ```go
   	// OnReturned receives, when an execution of the subagent ends, every
   	// message the user sent it that it never took, in the order they were
   	// written. The subagent is idle then and takes no messages, so the surface
   	// gives them back to the user — in the conversation's own composer.
   	OnReturned func(agentKey string, returned []Input)
   ```
7. 删除撤回整条路径与其 plumbing（见"现状"的清单；`subagentExecution` 只留 `executionID`、`cancel`、`boundary`）；`withdraw` 删除后，
   `interruptToSend`、`cancelRunning` 只读写这三个字段。删 `SubagentCompactTarget`。`EventStreamSink` 本身不改，只是 `subagentRunContext` 不再传 `onResponseStarted`。

**Verify**：`CGO_ENABLED=1 go build -tags fts5 ./pkg/run ./pkg/process` → exit 0（`pkg/tui`、`pkg/gateway` 此时编译失败是预期，Step 4/5 修）；Step 1 的 4 个测试 `--- PASS`。

### Step 3：迁移 `pkg/run` 里依赖旧语义的测试

逐个处理（不在表里的、因签名变化编译不过的测试，按"只改调用形状、不改断言意思"修）：

| 测试 | 处理 |
|---|---|
| `TestModelContinuationWaitsForTheUsersExecutionOfTheSameSubagent` | 用 `StartSubagentAutoContinue` 起第一个执行；改名 `TestModelContinuationWaitsForTheRunningExecutionOfTheSameSubagent` |
| `TestUserMessageContinuesAFailedSubagent` | 删除（由 `TestUserCannotStartAnIdleSubagent` 取代） |
| `TestEverySubagentExecutionIsReportedToItsScheduler` | 第三条路径（用户消息）改为 `StartSubagentAutoContinue`；140 加的 `AwaitedByDispatcher` 断言保持 `true true false` |
| `TestUserCanTalkToEveryKindOfSubagent` | 删除（同上） |
| `TestUserSubagentExecutionNeverTouchesThePrimaryConversation` | 起执行改用 `StartSubagentAutoContinue`；改名 `TestSubagentContinuationNeverTouchesThePrimaryConversation`；断言不变 |
| `TestInterruptSubagentToSendRunsThePendingSteersNext` | 删除（由 `TestInterruptToSendContinuesInTheSameExecution` 取代） |
| `TestSubagentViewQueueQuestionsAnswerFromTheLiveChannel`、`TestSubagentQueueChangesReachTheSurface`、`TestSubagentQueueKeepsNoDeliveredSteers` | `send("start", …, SubagentDeliveryStarted)` 改为 `StartSubagentAutoContinue`；`OnBoundary` 换 `OnReturned`；follow-up 模式的那几次发送改成插话并期望 `SubagentDeliverySteered` |
| `TestWithdrawBeforeTheSubagentAnswers`、`TestWithdrawnUserExecutionPublishesItsEndSoTheSurfaceRetiresIt` | 删除 |
| `TestUserMessageReusesTheSubagentsPrefix` | 起执行改用 `StartSubagentAutoContinue`；改名 `TestSubagentContinuationReusesTheSubagentsPrefix` |
| `TestSubagentOfAnotherConversationIsNotFound` | 只改调用签名；仍期望 `ErrSubagentNotFound`（查找在空闲判断之前） |
| `TestUserSkillCommandActivatesTheSkillInTheSubagent` | 起执行改用 `StartSubagentAutoContinue(…, Input{…SkillName…})`；改名 `TestSubagentContinuationActivatesTheSkillItSelected` |
| `TestUserDrivenExecutionEndsWithTheSpawnIdentity` | 起执行改用 `StartSubagentAutoContinue`；断言不变 |
| `TestContinueAfterCustomSubagentDisabledSaysDisabled`、`TestUserMessageToDeletedCustomSubagentIsRefused` | 只改调用签名；期望不变（失效的自定义类型拒绝在空闲判断之前） |
| `TestSubagentCompactTargetRefusesARunningSubagent`、`TestSubagentManualCompactionReusesTheSubagentsPrefix`、`TestForkSubagentManualCompactionReusesItsFrozenPrefix` | 若只测 `SubagentCompactTarget` / 子代理手动压缩：删除；若同时测了 `CompactionService` 对 worker session 的通用行为（与视图无关），保留那部分、去掉对 `SubagentCompactTarget` 的调用 |

**Verify**：`FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/run ./pkg/process -count=1` → 与基线相比无新增 FAIL（被删测试不算）。

### Step 4：turn 与网关

1. `pkg/turn/slash.go:86` `compact` 改为 `SubagentView: SubagentViewHidden`；`model.go` 注释改为 "(/context)"；`slash_test.go` 期望表里 `"compact": SubagentViewHidden`。
2. 网关：
   - 删除 `withdraw`、`compact` 两条路由及 `handleSubagentWithdraw`、`handleSubagentCompact`；
   - `subagentCommandInput`：`case "compact", "context":` 改为 `case "context":`（`/compact` 现在是内建 + Hidden，走 `subagentViewCommandCode`）；
   - `handleSubagentInput`：不再读 `req.Mode`（`subagentInputRequest.Mode` 字段可保留以兼容旧前端、注释写明被忽略，或删除——二选一，删除则 143 同步删请求里的 `mode`）；
     `run.SendToSubagent` 新签名；`errors.Is(err, run.ErrSubagentNotRunning)` → `writeSubagentViewError(w, subagentNotRunningCode, "the subagent is not running", http.StatusConflict)`，
     新增常量 `subagentNotRunningCode = "subagent_not_running"`；
   - `subagentConversationSurface`：`OnReturned: func(agentKey string, returned []run.Input) { s.publishSubagentQueuedInputReleased(sid, agentKey, nil, returned) }`
     （`publishSubagentQueuedInputReleased` 的签名可保持，`Next` 恒为空；或改成两参数——保持事件载荷字段不变，网页按 `inputs` 处理）。
3. 网关测试：`TestSubagentInputStartsAnIdleSubagent` 改为 `TestSubagentInputRefusesAnIdleSubagent`（409 + `subagent_not_running`）；
   `TestSubagentInputRefusesConversationCommands` 里 `/compact` 期望改为 `subagentViewCommandCode`；删除 `TestSubagentCompactRefusesWhileRunning`；其他因路由删除而 404 的测试一并删除或改写。

**Verify**：`FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/turn ./pkg/gateway -count=1` → 与基线相比无新增 FAIL。

### Step 5：终端

1. `Session` 接口：`SendToSubagent(sessionID, agentKey string, submission ComposerSubmission) (run.SubagentDelivery, error)`（去掉 `followUp`）；删 `WithdrawSubagentInput`、`CompactSubagent`；
   新增 `SubagentRunning(sessionID, agentKey string) bool`（`ChatSession` 实现直接 `run.SubagentRunning(s.runner(), …)`——它只读本进程内存里的通道，可以每次重绘调用）。`fakeSession` 同步（加一个 `subagentRunning map[string]bool`）。
2. `chat_session.go` `subagentSurface`：`OnReturned` → `s.notifyUI(SubagentInputReturnedMsg{AgentKey, Returned: subagentSubmissions(returned)})`；`notify.go` 里 `SubagentInputBoundaryMsg` 换成
   `SubagentInputReturnedMsg{AgentKey string; Returned []ComposerSubmission}`。
3. `run.go`：
   - `handleSubagentInputBoundaryMsg` 换成 `handleSubagentInputReturnedMsg`：把 `Returned` 交给新函数 `state.returnToMainComposer(renderer, view, items)`；
   - 新增 `returnToMainComposer`：主视图正在屏幕上（`renderer.ActiveView() == ""`）就并进当前输入框（与当前草稿合并，照旧 `handleSubagentInputBoundaryMsg` 里 Restore 的写法），
     否则 `state.prependRestoredToViewDraft("", items)`；并在该子代理视图里画一行系统提示：`subagentUnsentReply = "The subagent finished; your unsent text is in the main composer."`
     （`Frame{Kind: FrameSystem, Title: "subagent", Content: subagentUnsentReply, AgentID: view, Final: true}`）；
   - `sendSubagentViewSubmission(…)` 去掉 `followUp`；`errors.Is(err, run.ErrSubagentNotRunning)` 时调 `returnToMainComposer` 而不是还原到子代理输入框；
   - 两处 `hotkeyQueueFollowUp` 的子代理视图分支：按插话发送（同 Enter）；
   - `handleSubagentViewEsc` 删掉撤回分支，顺序变成：中断并发送 → 取消自动续跑 → 返回主视图；
   - `subagentViewSlash`：删 `case "compact"` 与 `dispatchSubagentCompactSlash`（`compact` 已是 Hidden，自动落到"回主视图"）。
4. 只读输入框：
   - `input_events.go` 新增 `var composerReadOnly atomic.Bool`（注释：子代理视图的子代理空闲时为 true，读键器不接受任何编辑）；在 `uiPanelActive` 吞字节的那处之前加
     `if composerReadOnly.Load() && b != 0x1b && b != 0x03 { pendingUTF8 = pendingUTF8[:0]; continue }`；`:1250` 的方向键条件加上 `|| composerReadOnly.Load()`。
     粘贴（bracketed paste）若走另一条路径写入草稿，同样在那里判断（Step 6 的测试会抓到）。
   - `render.go` `ComposerRenderState` 加 `ReadOnly bool`（注释同上）；`reducer.go` `buildComposerBlock` 开头：`if cs.ReadOnly { return r.buildReadOnlyComposerBlock(cs, statusText, footerText, termWidth) }`，
     新函数只排：状态行（有就照 `buildNormalComposerBlock` 的写法前置空行）、footer 一行、roster（可见时，照原写法）；`cursorRow: -1`，不画边框和输入行。
   - `renderComposerWithState`：`syncComposerToView` 之后算 `idle := view != "" && !state.session.SubagentRunning(state.sessionID, view)`；
     `idle` 时若当前输入框有草稿（`state.currentDraftSubmission()`），先 `returnToMainComposer` 并清空当前输入框、`state.seedReader("", seedCursorEnd)`；
     `composerReadOnly.Store(idle)`；两处 `RenderComposerState(ComposerRenderState{…})` 都带 `ReadOnly: idle`。
5. `notify.go` `autoContinueTranscriptText(code, at, now)` 加参数 `typingCancels bool`：true 结尾 `" · esc or type to cancel"`，false 结尾 `" · esc to cancel"`；
   `announceAutoContinue` 对主视图（`view == ""`）传 true、子代理视图传 false；`notify_test.go` 里两处调用/期望同步（主视图文本不变）。

**Verify**：`CGO_ENABLED=1 go build -tags fts5 ./...` → exit 0；`gofmt -l pkg` 无输出。

### Step 6：终端测试

删除 `TestEscInASubagentViewWithdrawsTheMessageItJustStarted`；`TestSubagentViewSlashCommandClasses` 改：`/compact` 期望"回主视图"一行、`fakeSession` 去掉 `compactSubagentOK`。新增：

1. `TestIdleSubagentViewHasNoInputBox`：`renderer.NoteSubagentSpawned("task-b", …)`、`SetActiveView("task-b")`、`fakeSession{subagentRunning: {"task-b": false}}`，调 `renderComposerWithState`；
   取 `renderer.buildComposerBlock(renderer.composerState, 100)`（或该文件已有的取画面办法）：`cursorRow == -1`、没有输入框边框行、有 `viewing` 开头的 footer；`composerReadOnly.Load()` 为 true。
   把 `subagentRunning["task-b"]` 改 true 再渲染：输入框回来，`composerReadOnly` 为 false。
2. `TestReaderTakesNoEditsWhileTheComposerIsReadOnly`（照 `input_events_test.go` 的 `TestRawReaderAdoptsRequestedCaretAndInsertsThere`）：`composerReadOnly.Store(true)`（`t.Cleanup` 还原），
   写入 `abc`、一个 bracketed paste `"\x1b[200~xyz\x1b[201~"`、一个方向上键 `"\x1b[A"`；断言在 300ms 内没有草稿事件里出现 `abc`/`xyz`；再写 `"\x1b"` 能收到 Esc 对应的事件。
3. `TestReturnedSubagentInputGoesToTheMainComposer`：主视图在屏幕上时处理 `SubagentInputReturnedMsg` → 主输入框草稿含退回的文字；子代理视图在屏幕上时 → `viewDrafts[""]` 含它、子代理视图里有 `subagentUnsentReply` 那一行。
4. `TestSendRefusedBecauseTheSubagentEndedMovesTheTextToTheMainComposer`：`fakeSession.subagentSendErr = run.ErrSubagentNotRunning`，在子代理视图提交一行 → 子代理输入框为空、`viewDrafts[""]` 含那行。
5. `TestFollowUpKeyInASubagentViewSendsASteer`：在运行中的子代理视图按 follow-up 热键 → `fakeSession.subagentSent` 恰 1 次。
6. `TestAutoContinueNoticeInASubagentViewSaysEscOnly`：子代理视图的公告文本以 `· esc to cancel` 结尾；主视图仍是 `· esc or type to cancel`。

**Verify**：`FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -count=1` → 与基线相比无新增 FAIL，6 个新测试 `--- PASS`。

### Step 7：工具与文档

1. `fake_provider.py` 删 `subagent-net` 模式（处理函数、模式说明、`SUBAGENT_NET_MAIN` 若只它用也删；`SUBAGENT_NET_PROBE_ARGS` 其他模式在用，保留）；`SKILL.md` 表格删该行；`driver.sh` 帮助删 `subagent-net = …` 那段与 `start` 的模式列表里的名字。
   `python3 -m py_compile .claude/skills/run-forebrain/fake_provider.py` → exit 0。
2. `docs/plan/SUBAGENT_CONVERSATION/README.md` 决策表之后加一段：
   ```markdown
   > **2026-10-26 修订（owner 裁定，plans/141–143）**：子代理视图**只在子代理有执行在跑时**接受输入，空闲时隐藏输入框、引擎拒绝（D2 不再成立；D3 的前提——用户发起的执行——不复存在）。
   > 执行结束时没送达的消息退回主对话输入框。D1 改为"中断并发送（在同一次执行里接着跑）→ 取消自动继续 → 返回主视图"，撤回删除。
   > 子代理视图里只有插话，没有"排到之后"；技能命令作为插话送达并激活技能（D4 的技能部分照旧生效，plans/141）；`/compact` 不再出现在子代理视图。
   > 额度恢复后的自动续跑（计划 011）照旧，经 `run.StartSubagentAutoContinue` 启动。
   ```

### Step 8：真机验收（run-forebrain 假模型驱动）

先读 `.claude/skills/run-forebrain/SKILL.md` 开头。**`$D build` 必须先跑。**

A. 空闲子代理的视图没有输入框：
```bash
D=.claude/skills/run-forebrain/driver.sh
$D build
ENABLE_SUBAGENT=1 FAKE_LIMIT_SECONDS=300 $D start subagent-limit 'probe answered'
$D submit 'go'
$D wait 'primary noted the failure' 40
$D screen | grep -n 'network probe'          # 记下任务行的行号 N（screen 的行号从 1 起，与 click 的行号一致）
$D click 5 <N>                               # 打开该子代理的视图
$D screen > "$TMPDIR/p142-idle.txt"; cat "$TMPDIR/p142-idle.txt"
$D send 'hello-from-idle'
$D screen | grep -c 'hello-from-idle'
$D key Escape
$D screen | tail -6
$D stop
```
**Verify**：`p142-idle.txt` 底部有 `viewing general-purpose` 的 footer、**没有**输入框（无边框与提示行）；`grep -c` 为 `0`；按 Esc 后回到主视图，主视图输入框正常。

B. 运行中可插话，"中断并发送"在同一次执行里接着跑：
```bash
rm -rf "$TMPDIR/p142-dump"
ENABLE_SUBAGENT=1 FAKE_DUMP_DIR="$TMPDIR/p142-dump" $D start subagent-steer 'sleep 30'
$D submit 'go'
$D wait 'running the slow step' 30
$D key Down; $D key Down; $D key Enter       # 打开子代理视图（以 SKILL.md 里 subagent-steer 的说明为准）
$D submit 'switch approach'
$D key Escape                                # 有待送插话：中断并发送
$D wait 'steer received' 30
grep -l 'switch approach' "$TMPDIR/p142-dump"/request-*.json
$D key Escape                                # 回主视图
$D screen | grep -c 'general-purpose task'
$D stop
```
**Verify**：`grep -l` 至少 1 个文件；主视图里关于这个子代理的卡片只有 1 张（`grep -c` 为 `1`，即没有因中断而多出一张"Running/Ran 1 general-purpose task"卡）；`$D provider-log` 里有 `SUBAGENT-STEER took the steer`。

驱动起不来按 SKILL.md 的 Troubleshooting；仍不行 STOP 6，**不要**跳过。

## Test plan

- 引擎新增 4 个（Step 1）；迁移/删除见 Step 3 表；网关改 2 删 1；终端新增 6 个、删 1 改 1。
- 回归：五包整包；`TestSubagentReceivesASteerAtItsNextToolBoundary`、141 的 `TestSteerTypedAsASkillCommandActivatesTheSkill`、140 的 `TestSubagentRunTellsTheDispatcherHowItsSubagentFailed` 必须仍绿。
- 真机 A/B。

## Done criteria

- [ ] Step 1 的 4 个、Step 6 的 6 个新测试 `--- PASS`；五包整包与基线相比无新增 FAIL（被删测试不算）
- [ ] build / vet / gofmt 通过；"死代码" grep 无输出；`python3 -m py_compile` 通过
- [ ] `grep -rn 'TurnInputModeFollowUp' pkg/run/subagent.go pkg/tui/chat_session.go pkg/gateway/api_extra.go` 无输出（子代理路径不再用 follow-up）
- [ ] 真机 A、B 如 Verify 所述
- [ ] `docs/plan/SUBAGENT_CONVERSATION/README.md` 修订段已加
- [ ] `git status --porcelain` 里本计划新增的改动只落在 In scope；`plans/README.md` 状态行已更新（除非评审方说由其维护）

## STOP conditions

1. 前置不满足，或按函数名找到的代码与"现状"描述不符（例如 140 的 `awaited` 不在 `runSubagentExecution` 里）。
2. Step 1 的测试在改代码前就通过。
3. 续跑那段需要的 fork 冻结系统提示不存在（`continueForkSubagent` 报 "fork continuation requires a frozen system prompt"）且出现在**非**测试装配里——说明首派走了执行器回退路径，报告，不要改 fork 的出生逻辑。
4. 任何插话/压缩/撤回（主对话）测试变红。
5. 基线跑不起来。
6. 真机驱动反复起不来，或 A/B 的结果与 Verify 不符。
7. 需要改 In scope 以外的文件（尤其 `frontend/`）。

## Maintenance notes

- "子代理在跑"只有一个定义：`run.SubagentRunning`（通道上有执行）。终端每次重绘读它；网关以 409 `subagent_not_running` 拒绝空闲时的输入。
- 一次执行 = 一段或多段尝试；只有"中断并发送"会开新的一段。新增会中断子代理的入口时，要决定它是"结束执行"（`BoundaryInterrupted`）还是"接着跑"（`BoundaryInterruptToSend`）。
- 自动续跑（011）是唯一一种用户与主代理都没发起的执行，入口只有 `StartSubagentAutoContinue`。
- 网页在 143 落地前会遇到 409（空闲时发送）与已删除的 withdraw/compact 端点（404）；这是本计划与 143 之间的过渡态，143 应紧接着执行。
