> 状态：**DONE（2026-11-27 execute＋tech lead APPROVE；执行器 alibaba/deepseek-v4.1-flash-1017，worktree `.worktrees/skill-command-steer-both-views`／同名分支，基线 dev `1d6726b6`，3 提交 `46c9d3c3`/`8fd2b013`/`29773e4a`，19 文件 +946/−84；已按 owner 指令 ff-only 落 dev `1d6726b6..29773e4a`（2026-11-27，未 push），落 dev 门禁复跑绿，worktree/分支已按 owner「删除」清理）**。验收记录见文末 `## Implementation Status`。

---

# 修复：Message queue 对「显式触发技能的 slash 命令」的 steer 语义——主代理视图 / 子代理视图 / Web 三端一致（含三项遗留收口）

- 类型：Bug（TUI message queue + 引擎 steer 投递/持久化 + Web 同类）
- 优先级 P1 · 工作量 M · 风险 MED（改 `pkg/run` 投递时序与 `pkg/state` 用户行 content 的取值，需引擎级回归 + tmux 真机）
- 基线：dev `1d6726b6`（工作区干净，2026-11-26 19:03）
- 流程：`plan-gated-bugfix`（批准后直接在当前源码实施，不用 worktree，改动留工作区不 commit）
- 落盘：批准后写 `plans/392-skill-command-steer-both-views.md`（当前最大编号 391）＋ `plans/README.md` 索引行

## owner 裁决（2026-11-26，按时间顺序，后者为准）

1. 报障原文：「必须定位根因并彻底修复bug，确保主代理视图和子代理视图的Message queue的语义、UI、UX完全一致。web端如果有同类问题，必须本期一并修复」。
2. 「遗留问题必须加入本期计划一并修复」→ §9 的遗留项并入本期。
3. 「web端完全不向用户暴露内置slash命令，内置slash命令只是给tui端用的，web端不需要，web端只支持通过slash命令显式触发技能」；「用户按下/键显示技能选择器，让用户选择技能」→ web composer 按 `/` 弹出的是**技能选择器**（只列技能）。
4. 终裁：「我说的是四项遗留问题的第一项 前端 slash_reply/picker 流 这个问题，本期不做任何修改，保持现状」→ **web 主视图运行中键入内置 slash 命令（/model、/compact…）被当纯文本 steer 给模型**这一项保持现状、不纳入本期；`/init`、`/compact`、`/goal` 三个内置命令在 webchat 的 `AllowedSurfaces`、「+」菜单的 /goal 项也不动。

---

## 1. 目标（Why this matters）

owner 报障（两张截图）：

1. **主代理视图** composer 运行中 Enter 发送 `/frontend-design 你可以评审…`，消息进了 **"Queued follow-up inputs"**（follow-up），而不是 steer。期望：与子代理视图一致，作为 steer 在下一个工具边界送达并激活技能。
2. **子代理视图** 同样的行正确入 steer，但出队后 **先显示 `Skill frontend-design` 卡、再显示历史用户卡**，且该用户卡丢了开头的 `/frontend-design` 和 `[Image #1]` 占位符，只剩后面的文字。

统一的语义（三端同一条规则）：**运行中 composer 里以显式触发技能的 slash 命令开头的消息 = 携带可信技能选择的 steer**。队列预览显示用户敲的原行（预览是单行 chip，空白折叠——`cleanInputText` 不动）；投递时转录顺序固定为 **you 卡（原行，含 `/skill` 与 `[Image #N]`）→ Skill 卡 → 模型输出**——与「空闲时以该命令开头的轮次」一致（提交即画 you 卡，preload 画 Skill 卡）；持久化的用户行与重放（TUI resume / web history）也按同一顺序显示原行。

并入的三项遗留（owner 裁决 2）：web 无附件 steer 的模型文本空白折叠（多行丢换行）、web 召回回复丢换行、技能 steer 的用户行重放显示展开文本而非 `/skill` 原行。

---

## 2. 根因（file:line 证据链）

### 2.1 问题 1：主视图进错车道（过时的守卫）

`pkg/tui/run.go:5526-5545`（`handleActiveRunSlash`）：

```go
		handled, continueRun, submission, _ := dispatchStreamSlashCommand(ctx, cmds, renderer, tracker, state, line, draft)
		if handled {
			if continueRun {
				// A steer joins an already-built agent session and cannot safely add
				// trusted run metadata. Queue explicit skill invocations as the next
				// turn so their activation is injected before that turn's first LLM
				// call instead of degrading back to prompt text.
				if strings.TrimSpace(submission.SkillPath) != "" || state.onlyUserShellCommandsRunning() {
					state.sessionQueue().FollowUp(queueInput(submission))
					state.resetComposerForActiveRunDraft()
					return
				}
				state.sessionQueue().Steer(queueInput(submission))
```

这段守卫写于「steer 还不能携带技能」的年代。**引擎早已支持技能 steer**：
- `pkg/run/turn_input.go:861-865`：`InputQueue.steer` 把 `in.SkillName/SkillPath` 写进 `TurnInputEntry`；
- `pkg/run/orchestration_llm.go:172-188`（`steerMessages`）：entry 带技能就在工具边界 `loadExplicitSkill`，激活消息插在 steer 之前；
- 注释原话「A steer typed as a skill command **in a subagent's view** first loads that skill」——能力只为子代理视图接通，主视图没跟上。

第二处缺口：`pkg/tui/run.go:3206-3212`（`queueInput`）根本不带技能字段：

```go
func queueInput(submission ComposerSubmission) run.Input {
	return run.Input{
		Text:    composerSubmissionPreview(submission),
		Parts:   append([]llm.ContentPart(nil), submission.Parts...),
		Payload: submission,
	}
}
```

对照子代理路径 `pkg/tui/chat_session.go:1711-1717`（`SendToSubagent`）是带 `SkillName/SkillPath` 的。**两处都改才算修到根**：只改 5534 的分支，steer 会带着 `applySlashOutcome` 展开后的请求（`run.go:5794/5810`、`turn/skills.go:101-112`：args，不含命令）进车道，但 `Input` 没有技能字段 ⇒ 引擎不加载技能、无 Skill 卡、模型只看到请求。

既有测试把 bug 钉成了契约：`pkg/tui/chat_session_test.go:22414 TestActiveRunQueuesExplicitSkillSlashInsteadOfSteering`（断言 `preview.Steers` 为空、`preview.FollowUp` 为 1）。

### 2.2 问题 2a：Skill 卡先于 you 卡（引擎投递时序）

`pkg/run/orchestration_llm.go:172-188`：`steerMessages` 在**取走 steer 时**（工具边界、模型调用之前）就调用 `loadExplicitSkill`，而 `loadExplicitSkill`（`pkg/run/skills.go:735-761`）当场通过 step hook 发出 `tool_started/tool_completed`（`ToolName:"skill"`）——这就是 Skill 卡。

而「已投递」（画 you 卡）按 `SteerDelivery` 契约发生在**下一次模型调用首个输出**（`orchestration_llm.go:1068-1088 withSteerDeliveryResponseStart` → `delivery.Commit()` → `notifyDelivered`），或 Esc 的 `CommitNow`（取走点）。四个取走点：`orchestration_llm.go:675-681`、`753-758`、`906-916`、`1023-1029`，全是 `w.steerMessages(ctx, delivery.Entries())` 之后才可能 Commit。

⇒ 顺序必然是 **Skill 卡 → you 卡 → 模型输出**，与空闲轮次「you 卡 → Skill 卡 → 输出」相反。you 卡的来源：
- 主视图 TUI：`InputQueue.steersDelivered`（`turn_input.go:739-764`）→ `q.hook` → `chat_session.go:1648` `InputQueueChangedMsg` → `run.go:492` / `5160` `renderDeliveredSteerMessages`；
- 子代理视图：`pkg/run/subagent.go:1019-1057 publishInputDelivered` → `RunEventSubagentInputDelivered` → `notify.go:1034` → `reducer.go:1876` 画 `FrameUser`。

两条路径都进同一个 UI FIFO（`notifyUI`），Skill 卡也经同一事件总线（TUI step hook `chat_turn.go:297-299/341-344` → `Events.Publish`；web `server.go:2604-2620` → `RunEvents().Publish`），所以**只要 Skill 卡的 step 事件改在 Commit 之后发出，顺序就对了**。

首 token 提交契约本身不能动：`turn_input.go:151-164` 写明取走即报「已投递」会让失败的调用把消息搁浅（表面已画 you 卡、消息却回队列）。

### 2.3 问题 2b：you 卡丢字（文本来源错误）

`pkg/run/subagent.go:1024-1031`：

```go
	for _, entry := range delivered {
		if entry.Mode != TurnInputModeSteer {
			continue
		}
		text := strings.TrimSpace(llm.TextWithoutImageSourceNotes(entry.Parts))
		if text == "" {
			continue
		}
```

`entry.Parts` 是**模型读的**内容：对技能 steer，`Parts[0]` = 展开后的请求（`turn.SkillCommandInput` 只返回 args，`pkg/turn/skills.go:81-91`），图片是 image part、`[Image #N]` 占位符已被 `stripComposerAttachmentPlaceholders` 去掉（`run.go:5810`、`2858`）。`TurnInputEntry` 上没有任何「用户原行」字段。

对照：主视图 TUI 用 Payload 的 `DisplayText`（`run.go:3392-3412 submissionTranscriptText`）；web 主视图用 `in.Text`（`run_control.go:423-428`，但 `cleanInputText` 已把空白折叠，`turn_input.go:1184`）。三处三个来源——这正是「不一致」的根源。

### 2.4 Web 同类问题（只读核实）

- **主视图运行中** `/skill …`：`frontend/src/composables/useChatStream.ts:6332-6337` → `queueActiveRunInput` → `POST /runs/:id/input` → `pkg/gateway/run_control.go:149-154` 直接 `q.Steer(in)`，**无技能解析** ⇒ 模型读整行原文、无激活、无 Skill 卡。
- **子代理视图**：`pkg/gateway/api_extra.go:3567-3575 subagentCommandInput` 把 `in.Text` **替换成展开文本** ⇒ 队列预览与 `subagent_input_delivered` 都丢 `/skill`。既有测试钉住：`api_extra_test.go:6113`（`Preview.PendingSteers == ["use probe-skill: go"]`）。
- 顺序问题（2a）在引擎层，web 一并受益。
- **`/` 选择器**：`frontend/src/components/ForebrainPromptTextarea.vue:385-386` 只在「`/` 不在句首」时过滤为技能；句首时把 webchat 允许的内置（`/init`、`/compact`、`/goal`，`pkg/turn/slash.go:92/93/108`）也列出。owner 裁决 3：按 `/` 显示技能选择器。

### 2.5 三项遗留的根因

- **L2 web 无附件 steer 空白折叠**：`withWebInputParts`（`run_control.go:1284-1298`）无附件时不建 `Parts` ⇒ `InputQueue.steer` 的 `steerParts` 回落 `llm.Text(in.Text)`，而 `in.Text` 已被 `cleanInputText`（`turn_input.go:1184`，`strings.Join(strings.Fields(...))`）折叠；多行消息到模型丢换行。释放的 follow-up 同理：`releasedInput`（`run_control.go:272-274`）用 `in.Text`，web 客户端据此重发。
- **L3 召回丢换行**：`queuedInputReply`（`run_control.go:76-84`）与 `handleSubagentQueuedInput`（`api_extra.go:3616`）回 `item.Text`（已折叠）。
- **L4 重放丢原行与错序**：steer 用户行由 `AppendMessageSequenceForRun` 写入，`storedContent`（`pkg/state/message_sync.go:103-105`）取消息 Parts 文本（模型读的）；`llm.Message` 没有 display 字段。空闲轮次靠 `UserTurn.RawInput`（`pkg/turn/session.go:273-316`：content 存原行、PartsJSON 存展开 parts）做到重放显示原行，steer 路径没有对应机制。web 把 `input_delivered` 视为「transcript-backed」在重放时跳过（`useChatStream.ts:3676-3678`），所以两端重放都读 `fb_messages.content`。
  **顺序**：steer 用户行随响应块结束的 `transcript.flush` 落库（`orchestration_llm.go` 各块末），而 Skill 卡的 step 事件在 Commit（首 token）时发出 ⇒ 事件早于用户行落库。TUI 重放对主代理 Skill 事件有专门锚点 `skillEventAnchorPosition`（`pkg/tui/commands.go:1462-1494`：放在「不晚于事件时间的最后一条 user 行」之后）——技能 steer 的 user 行晚于事件，锚点会落到**本轮最初那条 user 行**之后，Skill 卡被重放到本轮更早的回答文字之前。⇒ 修法：提交时**先把含 steer 消息的 session flush 进转录，再发 Skill 卡事件**（§3.1-D 的 `onCommit` 顺序），用户行落库早于事件，锚点自然落在 steer 行之后；子代理视图的 worker 转录与 web 历史同理（事件按时间插在 user 行与回答行之间）。

---

## 3. 修复设计

### 3.1 引擎 `pkg/run`

**A. `Input` 增 `Display`**（`pkg/run/controller.go:33-66`，加在 `Payload` 之前）：

```go
	// Display is the message as its sender wrote it — the composer's line, its
	// slash command and image chips included — for a surface to draw when the
	// message is delivered, and for the user row to keep. Text is only the
	// queue's preview (whitespace collapsed) and the model's fallback; Parts
	// are what the model reads. A skill command steers with the command in
	// Display and only its request in Parts, so a surface drawing Parts would
	// lose the command. Empty for producers that keep none; the parts' text
	// then stands in.
	Display string
```

`cloneInput`（`turn_input.go:1210-1215`）按值复制，自动带上。

**B. `TurnInputEntry` 增 `Display`**（`turn_input.go:22-37`），逐字段拷贝处各加一行 `Display: entry.Display` / `in.Display`：
`turn_input.go:83`（`enqueueEntry`）、`115-122`（`takeSteersLocked`）、`507-514`（`DrainAll`）、`561-568`（`Snapshot`）、`861-865`（`q.steer` 构造 entry）；`subagent.go:1154-1165 steerEntries` 加 `Display: in.Display`。

**C. `SteerDelivery` 增提交后回调**（`turn_input.go:169-180` 结构体、`228-244 Commit`）：

```go
	// onCommit runs once the entries are reported delivered (see Commit), on
	// the goroutine that committed, before the call's output reaches the
	// surface: the Skill card of a skill-carrying steer goes here, so the
	// person's message is drawn ahead of the activation it caused. A delivery
	// that rolls back never runs it.
	//
	// Every way a delivery commits runs it — the call's first output
	// (withSteerDeliveryResponseStart), a non-streaming success, an incomplete
	// response that is nevertheless the answer, the retry layer's completed
	// part, and Esc's CommitNow — because they all settle through Commit.
	onCommit func()
```

```go
// OnCommit registers what to publish once this delivery commits. Set before
// the call carrying the entries begins, so it is in place under the runtime's
// lock by the time Commit reads it.
func (d *SteerDelivery) OnCommit(fn func()) {
	if d == nil || fn == nil {
		return
	}
	d.rt.mu.Lock()
	d.onCommit = fn
	d.rt.mu.Unlock()
}
```

`Commit()` 末尾（`r.notifyDelivered(entries)` 之后、`return true` 之前）：

```go
	if fn := d.onCommit; fn != nil {
		fn()
	}
```
（`onCommit` 在锁内读出：`entries := d.entries` 旁边加 `fn := d.onCommit`。）
`CommitNow` → `Commit` 自动带上；`Rollback`（`270-290`）不触发。`Commit` 的全部入口：`orchestration_llm.go:536`（`beforeStart`）、`:589`（非流式成功）、`:601`（incomplete 已完成）、`:1080`（`OnResponseStarted`）、`CommitNow`×4 —— 回调在 `Commit()` 内部，入口全被覆盖；`OnCommit` 在 `steerMessages` 返回前注册，而承载该 delivery 的调用、`withSteerDeliveryResponseStart` 与 `beforeStart` 闭包都在下一轮迭代才建立，不存在「先 Commit 后注册」的窗口。

**D. `steerMessages` 改为「先装填、提交后先落库再发卡」**（`orchestration_llm.go:172-188`）：

```go
func (w *toolOrchestrationLLM) steerMessages(ctx context.Context, entries []TurnInputEntry) ([]llm.Message, []tool.StepEvent) {
	// The Skill card is published with the delivery, not with the load (see
	// publishSteerOnCommit): the person's message is drawn first and the
	// activation it caused after it — the order a turn that starts with the
	// command draws (the user card at submission, the Skill card in preload).
	// A load the call rolls back showed no card, so the attempt that finally
	// carries the steer is the one that shows it, once.
	var held []tool.StepEvent
	loadCtx := tool.WithStepHook(ctx, func(_ context.Context, evt tool.StepEvent) { held = append(held, evt) })
	out := make([]llm.Message, 0, len(entries))
	for i, entry := range entries {
		name, path := strings.TrimSpace(entry.SkillName), strings.TrimSpace(entry.SkillPath)
		if name != "" && path != "" {
			stepID := fmt.Sprintf("skill-steer-%d-%d", i, time.Now().UnixNano())
			selection := explicitSkillSelection{SkillName: name, SkillPath: path}
			if activation, err := loadExplicitSkill(loadCtx, w.state, selection, stepID); err == nil {
				msg := explicitSkillActivationMessage(activation)
				msg.Ephemeral = false
				out = append(out, msg)
			}
		}
		out = append(out, steerMessage(entry))
	}
	return out, held
}

// publishSteerOnCommit registers what a skill-carrying delivery publishes once
// it commits, in this order: the transcript first, so the user's row is on
// disk before the Skill card's events — a replay anchors the card after the
// latest user row no later than the event (skillEventAnchorPosition), which
// is the steer's own row only if that row landed first; then the held step
// events, so the card is drawn ahead of the call's output. A delivery that
// rolls back runs neither: nothing of a load the call abandoned is written.
// The flush runs on the stream goroutine that committed, while the loop is
// parked inside its model call; AppendMessageSequenceForRun is the same
// transactional reconcile every block-end flush uses.
func (w *toolOrchestrationLLM) publishSteerOnCommit(ctx context.Context, delivery *SteerDelivery, transcript *transcriptProgress, session *[]llm.Message, model string, held []tool.StepEvent) {
	if len(held) == 0 {
		return
	}
	step := tool.StepHookFromContext(ctx, w.state)
	delivery.OnCommit(func() {
		transcript.flush(context.WithoutCancel(ctx), *session, model, nil)
		for _, evt := range held {
			if step != nil {
				step(ctx, evt)
			}
		}
	})
}
```

四个取走点把 `session = append(session, w.steerMessages(ctx, delivery.Entries())...)` 改为两步：
```go
			msgs, held := w.steerMessages(ctx, delivery.Entries())
			session = append(session, msgs...)
			// …既有的 inFlightSteers / partialCapture.Set(session) 顺序不变…
			w.publishSteerOnCommit(ctx, delivery, transcript, &session, modelForTranscript(), held)
			delivery.CommitNow()
```
（`orchestration_llm.go:677/756/911/1026`；`session` 是循环局部切片变量，闭包捕获 `&session`，提交时读到的就是含 steer 消息的那份；`modelForTranscript` 是 `:235` 的闭包；`transcript` 是 `:227` 的 `newTranscriptProgress`，无 store 时为 nil、`flush` 空转。）`held` 为空（无技能的 steer）不注册回调，落库时机与今天完全相同。`step` 为 nil 时 `loadExplicitSkill` 原本就空转，flush 循环加 `if step != nil`。

函数注释（`160-171`）里「in a subagent's view」改为两视图通用措辞。

**安全性（两轮评审已核）**：取走到 Commit 之间没有任何 step 事件的消费者——所有 step hook（TUI `chat_turn.go:235`、gateway `server.go:2599`、`approval.go:121`、`subagent.go:2345`、`run_executor.go:449`）只做「持久化 canonical 事件 + 通知 UI」，全仓无 step ledger；session 消息、`partialCapture`、压缩计数只依赖消息位置。提交时的提前 flush 只是把「首输出之后本就要写」的 activation＋steer 两行提前到首输出时刻写（activation 是 `IsMeta` 的 user 行、steer 路径 `Ephemeral=false`，`skills.go:649-672`、`orchestration_llm.go:180-182`；`stripUnpersistedMessages` 只剔 system/Ephemeral，所以它本来就会落库），对齐逻辑（按 PartsJSON）让块末的常规 flush 不会重写它们；`AppendMessageSequenceForRun` 本就是 IMMEDIATE 事务、允许多写者。flush 用同一个 `ctx` 对象（`RunIDFromContext`/`HookAgentIDFromContext` 读到相同值）；工具 step hook 本来就从工具 goroutine 与流 goroutine 调用，`Commit` 调钩子时不持 `rt.mu`。
**时延权衡**：这次 flush 是首 token 关键路径上的一次同步 DB 事务（Commit → flush → 发卡 → 转发输出）。本地 SQLite 亚毫秒；远端 MySQL/PG 会给每次技能 steer 的投递加一次写往返（该写本就要发生、只是从块末提前到首 token）。非技能 steer 不走这条路，不受影响。接受理由：换的是重放顺序正确（否则 Skill 卡锚到本轮最初的 user 行之后）；若真机或压测显示远端库下 TTFT 可感知，再评估改为异步 flush（会重新引入落库晚于事件的风险，届时单独立项）。

**E. `publishInputDelivered` 用原行**（`subagent.go:1024-1031`）：

```go
		// The person's message is drawn as they wrote it — the line with its
		// skill command and image chips — which is what the queue kept in
		// Display; the parts carry what the model reads, which for a skill
		// command is only its request. A peer letter travels as its envelope
		// either way.
		text := strings.TrimSpace(entry.Display)
		if text == "" {
			text = strings.TrimSpace(llm.TextWithoutImageSourceNotes(entry.Parts))
		}
```

**P.（L4）steer 用户行保留原行**
- `pkg/llm/llm.go:311-351` `Message` 增（紧随 `Origin`，同一类「持久化元数据」）：
```go
	// DisplayText is what the person typed, when it differs from the text the
	// model reads — a skill command's line against the request it expanded
	// into. It is persistence metadata only, like Origin: the user row's
	// content column carries it so a replay shows the command the person
	// entered, while PartsJSON keeps the model's parts. No provider adapter
	// may read it or serialize it into a request.
	DisplayText string `json:"display_text,omitempty"`
```
- `pkg/state/message_sync.go:103-105` `storedContent`：
```go
func storedContent(msg llm.Message) string {
	if strings.TrimSpace(msg.Role) == llm.RoleUser {
		if display := strings.TrimSpace(msg.DisplayText); display != "" {
			return dbx.ValidText(display)
		}
	}
	return dbx.ValidText(strings.TrimSpace(llm.TextContent(storedParts(msg)...)))
}
```
  PartsJSON 不变：`MessagePartsJSON(msg, content)` → `contentBlocksJSON(storedParts(msg), fallback)`（`usage.go:117`）在 Parts 非空时只序列化 Parts（模型文本）。追加对齐不受影响：`storedTranscriptMessages` 用 `ParseMessage(role, content, partsJSON)` 重建，`ParseMessageParts(partsJSON, content)` 优先 partsJSON（`message_parts.go:371-379/524`），`messagesAlign` 的 user 分支按 `messageText` 比较的是模型文本（`message_sync.go:361-366`）。与空闲轮次 `PersistUserTurn`（content=RawInput、PartsJSON=展开 parts）同一形状。
- `orchestration_llm.go:194-201` `steerMessage`：`msg.DisplayText = strings.TrimSpace(entry.Display)`。

### 3.2 TUI `pkg/tui`

**F. `queueInput` 带齐字段**（`run.go:3206-3212`）：

```go
func queueInput(submission ComposerSubmission) run.Input {
	return run.Input{
		Text:      composerSubmissionPreview(submission),
		Display:   strings.TrimSpace(submission.DisplayText),
		Parts:     append([]llm.ContentPart(nil), submission.Parts...),
		Payload:   submission,
		SkillName: strings.TrimSpace(submission.SkillName),
		SkillPath: strings.TrimSpace(submission.SkillPath),
	}
}
```

**G. `handleActiveRunSlash` 删掉技能→follow-up 分支**（`run.go:5529-5541`）：

```go
			if continueRun {
				// A user shell command is not steerable: steering it would
				// attach the message to a shell invocation that never consults
				// the model, so it waits for its own turn. Everything else the
				// command expands into steers — an explicit skill included: the
				// steer carries the trusted selection, the turn loads the skill
				// at the boundary that takes it, and the view shows the user's
				// line, the Skill card, then the answer, in both views alike.
				if state.onlyUserShellCommandsRunning() {
					state.sessionQueue().FollowUp(queueInput(submission))
					state.resetComposerForActiveRunDraft()
					return
				}
				state.sessionQueue().Steer(queueInput(submission))
				state.resetComposerForActiveRunDraft()
				return
			}
```

**H. `SendToSubagent` 带 `Display`**（`chat_session.go:1711-1717`）：加 `Display: strings.TrimSpace(submission.DisplayText),`。

### 3.3 Web `pkg/gateway`（技能同类 + L2/L3）

**I. `runInputRequest.input()` 带 `Display`**（`run_control.go:70-72`）：`Display: strings.TrimSpace(req.Message)`。

**J. 共享技能解析 `skillCommandPrompt`**（新函数，放 `api_extra.go` `subagentCommandInput` 旁）：

```go
// skillCommandPrompt resolves a leading skill command on a message about to
// steer a running turn or subagent — one rule for both views: the trusted
// selection rides on the input, and the prompt the command expanded into is
// what the model reads, while Text and Display keep the line as typed — the
// queue previews it and the transcript draws it, the way the terminal does for
// either view. A built-in command is left alone (it belongs to the
// conversation, and the caller decides what that means for its view), and a
// line that is no skill is the message itself: both come back empty, meaning
// "nothing to expand — the message is its own model text". Never runs a
// built-in here: parseSlashCommandWithOptions executes what it parses.
func (s *Server) skillCommandPrompt(in *run.Input) string {
	line := strings.TrimSpace(in.Text)
	if !strings.HasPrefix(line, "/") {
		return ""
	}
	fields := strings.Fields(line)
	if len(fields) == 0 {
		return ""
	}
	name := strings.ToLower(strings.TrimPrefix(fields[0], "/"))
	if name == "" || turn.IsBuiltinName(name) {
		return ""
	}
	outcome := parseSlashCommandWithOptions(s, "", "webchat", line, turn.Context{}, nil)
	if strings.TrimSpace(outcome.SkillName) == "" && strings.TrimSpace(outcome.SkillPath) == "" {
		return ""
	}
	in.SkillName = strings.TrimSpace(outcome.SkillName)
	in.SkillPath = strings.TrimSpace(outcome.SkillPath)
	return strings.TrimSpace(outcome.ContinueInput)
}
```

（`parseSlashCommandWithOptions` 对技能命令是纯函数：`turn/skills.go:80-91` 的 handler 只返回 `Result`；动态命令注册表唯一来源是技能，`ReplaceDynamicSource` 全仓仅 `turn/skills.go:41` 一处调用。`turn.IsBuiltinName` 同时覆盖已移除命令名——web 主视图运行中的内置命令按 owner 终裁保持现状：原文 steer，不在这里执行。）

**K.（L2）`withWebInputParts` 接收模型文本并无附件时也建 Parts**（`run_control.go:1284-1298`）：

```go
// withWebInputParts gives the message the parts the model reads: modelText —
// the prompt a skill command expanded into, empty when the message is its own
// model text — followed by what it attached, resolved into image parts. The
// parts carry the words as written: the queue's own text is only a preview,
// whitespace collapsed, and the model must not read a multi-line message as
// one line.
func (s *Server) withWebInputParts(ctx context.Context, runner *run.Runner, sessionID string, in run.Input, modelText string) (run.Input, error) {
	if modelText == "" {
		modelText = in.Text
	}
	if len(in.Attachments) == 0 && len(in.MentionImages) == 0 {
		if strings.TrimSpace(modelText) != "" {
			in.Parts = []llm.ContentPart{llm.Text(modelText)}
		}
		return in, nil
	}
	turnInput, err := s.prepareWebTurnInput(ctx, sessionID, modelText, in.Attachments, in.MentionImages)
	// …其余不变
```

调用点：`run_control.go:149`、`api_extra.go:3522`。

**L. `handleRunInput` 解析技能**（`run_control.go:149-154`）——拆成两条语句：

```go
	in := req.input()
	modelText := s.skillCommandPrompt(&in)
	in, err := s.withWebInputParts(r.Context(), s.runnerFor(r.Context(), sessionID), sessionID, in, modelText)
```

**M. `handleSubagentInput` / `subagentCommandInput` 不再覆盖 `in.Text`**（`api_extra.go:3511-3522`、`3553-3577`）：

```go
	in := run.Input{Text: strings.TrimSpace(req.Message), Display: strings.TrimSpace(req.Message), Attachments: req.Attachments, MentionImages: req.MentionImages}
	modelText := ""
	if strings.HasPrefix(in.Text, "/") {
		expanded, code, ok := s.subagentCommandInput(&in)
		if !ok {
			writeSubagentViewError(w, code, "this command belongs to the conversation", http.StatusBadRequest)
			return
		}
		modelText = expanded
	}
```
```go
func (s *Server) subagentCommandInput(in *run.Input) (modelText, code string, ok bool) {
	fields := strings.Fields(strings.TrimSpace(in.Text))
	name := strings.ToLower(strings.TrimPrefix(fields[0], "/"))
	switch name {
	case "context":
		return "", useDedicatedEndpointCode, false
	}
	// The registry lists skill commands beside the built-ins, so a name it
	// finds may still be a skill: only the built-in table itself says
	// otherwise. A built-in that acts on the conversation (hidden, or global)
	// is not sent here; the page runs it in the main view instead.
	if turn.IsBuiltinName(name) {
		return "", subagentViewCommandCode, false
	}
	modelText = s.skillCommandPrompt(in)
	if strings.TrimSpace(in.SkillName) == "" && strings.TrimSpace(in.SkillPath) == "" {
		return "", subagentViewCommandCode, false
	}
	return modelText, "", true
}
```

**N. `watchRunQueue` 的 `input_delivered` 用原行**（`run_control.go:424-428`）：`Text: firstNonBlank(in.Display, in.Text),`（`firstNonBlank` 在 `1300-1307`）。

**O.（L2）释放的 follow-up 用原行**（`run_control.go:272-274`）：`event.ReleasedInput{Text: firstNonBlank(in.Display, in.Text), …}`——web 客户端据此重发，多行不丢换行。

**Q.（L3）召回回复用原行**：`queuedInputReply`（`run_control.go:79`）与 `handleSubagentQueuedInput`（`api_extra.go:3616`）的 `"message"` 改为 `firstNonBlank(item.Display, item.Text)`。

**R.（裁决 3）web `/` 选择器只列技能**（`frontend/src/components/ForebrainPromptTextarea.vue:383-386`）：

```ts
    // Pressing "/" opens the skill picker: only skills are offered, wherever
    // the "/" stands in the draft (owner, 2026-11-26).
    slashCommands.value = records.filter((record) => record.category === 'skill')
```
（删掉 `slashAtStart` 的区分；`hintCommand` 的参数提示与「+」菜单不动。子代理 tab 的记录本就 `view=subagent` 只含技能。）

### 3.4 行为变化矩阵（★ = 刻意修订）

| # | 场景 | 旧行为 | 新行为 | |
|---|---|---|---|---|
| 1 | TUI 主视图，运行中 Enter `/skill 请求…` | follow-up（"Queued follow-up inputs"），轮次结束后作下一轮 | steer（"Messages to be submitted after next tool call"）；边界送达并激活；转录 you 卡（原行）→ Skill 卡 → 回答 | ★ owner 裁决 |
| 2 | TUI 主视图，运行中 `/skills` 选择器选中技能（`chat_slash.go:1917-1923 runSkillNow` 产出带 SkillName 的 submission） | follow-up | steer（同 #1） | ★ 同一语义 |
| 3 | TUI 主视图，运行中 Enter `/init`、`/plan <desc>`、`/goal <obj>`（ShouldContinueRun 无技能） | steer | 不变 | |
| 4 | TUI 主视图，仅用户 shell 在跑时 Enter 任意行 | follow-up | 不变 | |
| 5 | TUI 主视图，Tab 排队 `/skill …`（未展开） | follow-up，释放后执行器 slash 阶段激活技能、原行作 you 卡 | 不变 | |
| 6 | TUI 子代理视图 Enter `/skill 请求…` | steer ✓；出队后 **Skill 卡 → you 卡**；you 卡文本 = 请求（丢 `/skill`、`[Image #N]`） | steer ✓；**you 卡（原行）→ Skill 卡 → 回答** | ★ 顺序＋文本 |
| 7 | TUI 主视图 steer 携带技能（#1 新可达） | — | 与 #6 完全相同 | ★ 一致 |
| 8 | 任意视图，技能 steer 的模型调用在首个输出前失败（429/5xx/网络）→ Rollback 重发 | Skill 卡已显示；重发再加载 ⇒ 第二张 Skill 卡 | 回滚那次**不显示任何卡**；最终承载它的调用提交时显示**一次** | ★ 顺带修正重复卡 |
| 9 | 任意视图，技能加载失败（文件被删/改名） | 失败卡在 you 卡之前 | 失败卡在 you 卡之后，steer 仍送达 | ★ 顺序 |
| 9b | 任意视图，Skill 卡出现的时刻 | 工具边界（取走 steer 时） | 与 you 卡同一时刻——下一次模型调用的首个输出，或 Esc 的立即发送；提交路径上多一次同步转录写（本地 SQLite 亚毫秒；远端库一次写往返，见 §3.1-D 时延权衡） | ★ 修复的固有结果 |
| 10 | 子代理视图，仅图片的 steer（TUI；parts 文本为空） | 不画 you 卡 | 画 you 卡 `[Image #1]`（主视图 `renderDeliveredSteerMessages` 本来就画） | ★ 一致 |
| 11 | peer 消息 steer（任何视图） | 文本 = 信封（parts） | 不变（Display 空 → 回落 parts） | |
| 12 | Web 主视图，运行中发送 `/skill 请求…` | steer 原文：模型读 `/skill …`、无激活、无 Skill 卡 | steer 携带技能选择：模型读展开请求；预览与 `input_delivered` 显示原行；you 气泡 → Skill 卡 → 回答 | ★ 同类 |
| 13 | Web 子代理视图发送 `/skill 请求…` | 预览与 `subagent_input_delivered` 文本 = 展开文本（丢 `/skill`） | 预览与 delivered 文本 = 原行；模型读展开文本 | ★ 同类 |
| 14 | Web 主视图 `input_delivered` 文本 | `in.Text`（空白折叠） | `in.Display`（原样含换行），无 Display 回落 `in.Text` | ★ 与子代理视图一致 |
| 15 | Web 主视图，运行中发送内置命令（`/model`、`/compact`…） | 原文 steer 给模型 | **不变**（owner 终裁 4） | 不纳入本期 |
| 16 | Web 子代理视图内置命令 | 400 拒绝（`subagentViewCommandCode`） | 不变 | |
| 17 | Web 主视图／子代理视图，无附件的多行 steer 到模型 | `steerParts` 回落折叠后的 `in.Text`，换行丢失 | Parts 携带原行，换行保留 | ★ L2 |
| 18 | Web 释放的 follow-up（客户端重发）、Tab/队列按钮排队的多行消息 | `releasedInput.Text` 折叠 | 原行（`Display`），重发后换行保留 | ★ L2 |
| 19 | Web 召回（edit_last）回填 composer 的文本 | `item.Text` 折叠 | `Display` 原行 | ★ L3 |
| 20 | 任意视图，steer 用户行的 `fb_messages.content` 与重放（TUI resume / web history） | 模型文本（技能 steer = 展开后的请求）；Skill 卡事件先于用户行落库，TUI 重放把 Skill 卡锚到本轮最初的 user 行之后 | 原行（`DisplayText`）；提交时先落库再发 Skill 卡事件，重放顺序 = you → Skill → 回答；PartsJSON 仍为模型 parts，追加对齐按 PartsJSON 不变 | ★ L4 |
| 21 | Web composer 按 `/` | 句首时列出 webchat 允许的内置（/init、/compact、/goal）＋技能 | 只列技能（技能选择器） | ★ owner 裁决 3 |

---

## 4. Scope

**In scope（恰好这些文件）**
- `pkg/llm/llm.go`（`Message.DisplayText`）
- `pkg/run/controller.go`（`Input.Display`）
- `pkg/run/turn_input.go`（entry 字段、5 处拷贝、`SteerDelivery.onCommit/OnCommit/Commit`）
- `pkg/run/orchestration_llm.go`（`steerMessages` 签名与实现、`steerMessage`、4 个取走点、注释）
- `pkg/run/subagent.go`（`steerEntries`、`publishInputDelivered`）
- `pkg/state/message_sync.go`（`storedContent`）
- `pkg/tui/run.go`（`queueInput`、`handleActiveRunSlash`）
- `pkg/tui/chat_session.go`（`SendToSubagent`）
- `pkg/gateway/run_control.go`（`runInputRequest.input`、`handleRunInput`、`withWebInputParts`、`watchRunQueue`、`releasedInput`、`queuedInputReply`）
- `pkg/gateway/api_extra.go`（`handleSubagentInput`、`subagentCommandInput`、新 `skillCommandPrompt`、`handleSubagentQueuedInput` 的 `"message"`）
- `frontend/src/components/ForebrainPromptTextarea.vue`（`/` 选择器过滤，一处）
- 测试：`pkg/run/turn_input_test.go`、`pkg/run/orchestration_llm_test.go`、`pkg/run/subagent_test.go`、`pkg/state/message_sync_test.go`、`pkg/tui/chat_session_test.go`、`pkg/gateway/api_extra_test.go`、`pkg/gateway/run_control_test.go`、`frontend/src/components/ForebrainPromptTextarea.test.ts`
- 计划落盘：`plans/392-skill-command-steer-both-views.md`、`plans/README.md`

**Out of scope（看起来相关但明确不碰）**
- **web 主视图运行中的内置 slash 命令**（`/model`、`/compact`…原文 steer）、前端 `slash_reply`/picker 流、`/init`／`/compact`／`/goal` 的 webchat `AllowedSurfaces`、「+」菜单 /goal 项——owner 终裁保持现状。
- `SteerDelivery` 的首 token 提交契约、`Rollback`/`RetractSteer` 语义（`turn_input.go:145-290`）不动。
- 空闲轮次的技能路径（`preloadExplicitSkills`、`runTurnWithSkill`、`applySlashOutcome`）不动。
- Esc 的 `interruptToSend` / 暂停后 `awaitUserMessage` 路径（`subagent.go:1120-1136`、`1325-1337`）：那里本就是先 `publishInputDelivered` 再起 attempt 加载技能，顺序已对，只随 `steerEntries` 带上 `Display`。
- `Input.Text` 的空白折叠语义（`cleanInputText`）不动——预览仍折叠；`steerParts` 的回落逻辑不动（web 已改为总是带 Parts）。
- `pkg/event` 的 payload 结构不动（`SubagentInputDeliveredPayload.Text` 语义从「模型读的」变为「发送者写的」，字段不变）。
- TUI 排队（Tab）未展开的 `/skill …`：释放后执行器 slash 阶段激活，原行作 you 卡——已正确，不动。

---

## 5. Steps（每步带验证）

> 构建/测试环境变量（每次 shell 都带）：
> `export PKG_CONFIG_PATH="$PWD/third_party/pdfium/$(go env GOOS)_$(go env GOARCH)"`
> （`third_party/` 已就位：libtokenizers / pdfium darwin_arm64 / silk。）
> 另一 agent（forebrain-harness-54）在同一树改前端模型下拉框；改 `ForebrainPromptTextarea.vue` 前 `peer_send` 打招呼，不碰它的文件。

### Step 0 — 复现（改任何生产代码之前）

0.1 drift check（必须全为 1）：
```bash
grep -c 'if strings.TrimSpace(submission.SkillPath) != "" || state.onlyUserShellCommandsRunning()' pkg/tui/run.go
grep -c '^func (w \*toolOrchestrationLLM) steerMessages(ctx context.Context, entries \[\]TurnInputEntry)' pkg/run/orchestration_llm.go
grep -c 'text := strings.TrimSpace(llm.TextWithoutImageSourceNotes(entry.Parts))' pkg/run/subagent.go
grep -c '^func (s \*Server) withWebInputParts(ctx context.Context, runner \*run.Runner, sessionID string, in run.Input)' pkg/gateway/run_control.go
grep -c 'if expanded := strings.TrimSpace(outcome.ContinueInput); expanded != "" {' pkg/gateway/api_extra.go
grep -c '^func storedContent(msg llm.Message) string {' pkg/state/message_sync.go
grep -c "slashCommands.value = slashAtStart.value ? records : records.filter" frontend/src/components/ForebrainPromptTextarea.vue
```
任何计数 ≠ 1 → STOP（基线漂移）。（第 6 条锚定函数签名：`storedContent` 的 return 行与 `messageText`（`:379`）同文，按 return 行 grep 会得 2。）

0.2 在 `pkg/run/orchestration_llm_test.go` `TestSteerTypedAsASkillCommandActivatesTheSkill`（:6206）之后新增 **引擎级复现** `TestSkillSteerIsDeliveredBeforeItsSkillCard`：同一 harness（`writeProbeSkill`、`toolpkg.WithStepHook`、`rt.AddChangeHook`），把 step 事件与 delivered 通知按发生顺序写进同一个带锁切片——step hook 追加 `"step:"+evt.ToolName+":"+evt.Kind`（**必须带 ToolName**：同一 hook 也会收到 `read_file` 的 started/completed，既有测试 :6247-6251 正是靠 `evt.ToolName != "skill"` 过滤），change hook 追加 `"delivered"`。断言：`"delivered"` 的下标 < 第一个 `"step:skill:tool_started"` 的下标；`ToolName=="skill"` 的 started/completed 各 1。
```bash
CGO_ENABLED=1 go test -tags fts5 ./pkg/run -run 'TestSkillSteerIsDeliveredBeforeItsSkillCard' -count=1
```
预期 **FAIL**（现状 skill step 先于 delivered）。

0.3 在 `pkg/run/subagent_test.go` `TestSubagentReceivesASteerAtItsNextToolBoundary`（:5919）之后新增 `TestSubagentSkillSteerReadsAsTypedBeforeItsSkillCard`：复用 `steerableSubagentExecutor`（:5569；结构体末尾追加可选字段 `steerWithSkill string`，非空时 steer `Input{Text: "/probe-skill "+s, Parts: [Text(s)], SkillName: "probe-skill", SkillPath: writeProbeSkill(t)}`——**基线上不带 `Display`**（该字段 Step 1 才加，带了整包编译失败会把 0.2 一起拖红）；给 `execGeneralSubagent` 的 ctx 装 `toolpkg.WithStepHook` 把 step 事件（带 `ToolName`）与 `probe` 的 `Publish` 写进同一个带锁顺序日志。断言：`subagent_input_delivered` 在 `tool_started(skill)` 之前；`probe.inputDelivered()[0].Text == "/probe-skill adjust approach"`。Step 3 时给该 `Input` 加 `Display: "/probe-skill "+s`，文本断言转绿。
```bash
CGO_ENABLED=1 go test -tags fts5 ./pkg/run -run 'TestSubagentSkillSteerReadsAsTypedBeforeItsSkillCard' -count=1
```
预期 **FAIL**（顺序反、文本 = `adjust approach`）。

0.4 `pkg/tui/chat_session_test.go:22414` 现有测试即问题 1 的复现（它把 bug 钉成契约）；跑一遍确认基线绿（它是"反向复现"）：
```bash
CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -run 'TestActiveRunQueuesExplicitSkillSlashInsteadOfSteering' -count=1
```
预期 **PASS**（证明基线确实把技能 slash 排成 follow-up）。

0.5 **L2 复现**（gateway）：`run_control_test.go` 新增 `TestRunInputKeepsLineBreaksForAMessageWithNothingAttached`（fixture 照 `:480`：`queue(t, s, runID)` 经 `Controller.Track` 挂了 runtime）：POST `{"message":"first\nsecond"}` → `q.Runtime().Snapshot()[0].Parts[0].Text == "first\nsecond"`。基线 **FAIL**：`withWebInputParts` 无附件时不建 Parts，`q.steer` 的 `steerParts` 回落 `llm.Text(in.Text)`，而 `in.Text` 已被 `cleanInputText` 折叠成 `first second`。
0.6 **L4 复现**（state）：`message_sync_test.go` 新增 `TestAppendMessageSequenceKeepsTheTypedLineAsUserRowContent`（照 `:18` 的写法）：
```go
	steer := llm.UserMessage(llm.Text("tighten it"))
	steer.DisplayText = "/probe-skill tighten it"
	canonical := []llm.Message{llm.UserMessage(llm.Text("go")), llm.AssistantMessage([]llm.ContentPart{llm.Text("answer")}), steer}
	// 追加两次同一列表
	// 断言：fb_messages 该 user 行 content == "/probe-skill tighten it"；ListTranscriptMessages 长度 3 且第 3 条 TextContent()=="tighten it"（模型上下文仍是展开文本）；行数不翻倍
```
基线编译失败（无 `DisplayText` 字段）即为红——**Step 0 里只有 0.6 允许编译红**；0.2/0.3/0.5 任何编译失败都算复现不成立。

复现结果与预期不符（0.2/0.3/0.5 在基线上 PASS 或编译失败，或 0.4 FAIL）→ STOP，根因判断有误，不得改生产代码。

### Step 1 — 引擎：Display 字段与提交后回调

1.1 改 `controller.go`（§3.1-A）、`turn_input.go`（§3.1-B/C：entry 字段、5 处拷贝、`onCommit/OnCommit/Commit`）、`subagent.go:steerEntries`（Display）。
1.2 `turn_input_test.go`：`TestInputQueuePeerOriginReachesTheModel`（:861）里两处 `w.steerMessages(context.Background(), <entries>)` 本步先不改（Step 2 改签名）。新增 `TestSteerCarriesItsDisplayTextToTheModelCall`：`q.Steer(Input{Text:"/probe go", Display:"/probe go", Parts:[Text("go")]})` → `rt.Snapshot()[0].Display == "/probe go"`；`rt.BeginSteerDelivery(ctx).Entries()[0].Display` 同。
```bash
gofmt -l pkg/run && CGO_ENABLED=1 go test -tags fts5 ./pkg/run -run 'TestInputQueue|TestSteer|TestController|TestSubagentDelivered|TestSteerCarries' -count=1
```
预期：gofmt 无输出；全部 PASS（现有契约不变）。

### Step 2 — 引擎：Skill 卡随投递发布（先落库、再发卡）

2.1 `orchestration_llm.go`：按 §3.1-D 拆出 `steerMessages`（返回消息＋held 事件）与新方法 `publishSteerOnCommit`；4 个取走点改成「`msgs, held := …` → append → `publishSteerOnCommit` → `CommitNow()`」两步；更新函数注释。
2.2 `turn_input_test.go:877/895` 两处改为 `msgs, _ := w.steerMessages(context.Background(), rt.DrainSteers())`（签名变了，断言不变）。
2.3 跑 Step 0.2 的复现 → 预期 **PASS**；既有 `TestSteerTypedAsASkillCommandActivatesTheSkill`、`TestSteerWhoseSkillFailsToLoadIsStillDelivered` → PASS。
2.4 **落库先于发卡**：新增 `TestSkillSteerRowIsWrittenBeforeItsSkillCard`（同 harness，但用 `wrapToolOrchestrationLLMWithProgress(inner, st, home, store, modelFn)`（`orchestration_llm.go:88`）接一个记录型 `transcriptProgressStore`——`AppendMessageSequenceForRun` 被调时先向同一个带锁顺序日志追加 `"flush:"+strings.Join(roles(msgs), ",")` 再委托给真实 `state.SessionStore`；step hook 追加 `"step:"+ToolName+":"+Kind`；change hook 追加 `"delivered"`）。断言顺序：`delivered` < 含 `user` 的 `flush` < `step:skill:tool_started`；`Execute` 结束后 `store.ListAllMessages` 里该 steer 的 user 行 `Content == "/probe-skill tighten it"`（给 `Input` 加 `Display`），且**非 meta 的 user 行恰 2 条**（`go` 与 steer）——技能 activation 是 `IsMeta` 的 user 行（`skills.go:649-672`，steer 路径 `Ephemeral=false`）也会落库，所以 user 行总数是 3；按 PartsJSON 的 `is_meta` 过滤后数（评审方实测 3 条 PASS）。基线上此测试在 Step 0 不写（依赖新签名），Step 2 新写即应 PASS；若 `"flush"` 不在 `"step:skill"` 之前 → 实现漏了 `publishSteerOnCommit` 的顺序，STOP。
   **fixture 前置（评审方实测踩坑）**：`fb_messages.run_id REFERENCES fb_runs(id)`（`schema_migrations.go:440`）。接真实 `state.SessionStore` 时必须先 `Ensure` 会话并用 `RunStore.CreateRun` 建一条 run 行、把**同一个 runID** 放进 ctx（`tool.WithRunID`），否则每次 flush 都 FOREIGN KEY 失败——而 `transcript_progress.go:67` 只记 WARN 不返回错，顺序断言照样过、行数断言得 0，极易误判实现有误。照 `runStreamFailure`（`orchestration_llm_test.go` 一带）建 store/run 的写法。
2.5 新增回滚用例 `TestSkillSteerCardIsShownOnceAfterARolledBackCall`：循环在 Rollback 后直接 `return res, err`（`orchestration_llm.go:788-797`），**重投递 = 第二次 `Execute`**。照现成范本 `failAfterToolCallLLM`（`orchestration_llm_test.go:4597`）与 `TestFailedSamplingReturnsDrainedSteerToQueue`（:4630）：第一次 `Execute` 在输出前失败 → 断言 `started == 0`（回滚那次不发卡）且 steer 回到队列、store 里没有该 user 行；第二次 `Execute` 成功 → `started == 1 && completed == 1`，且 delivered 仍先于 skill 卡。
```bash
gofmt -l pkg/run && CGO_ENABLED=1 go test -tags fts5 ./pkg/run -run 'Steer|Skill|Rollback|Retract' -count=1
```
预期全 PASS。

### Step 3 — 引擎：子代理投递用原行

3.1 `subagent.go publishInputDelivered` 按 §3.1-E。
3.2 更新 `subagent_test.go:10358 TestSubagentDeliveredPayloadLeavesTheUsersOwnMessageUnmarked`：`SendToSubagent` 的 `Input` 加 `Display: "go on\ntake your time"`（原行保留换行），断言不变（换行保留的契约由 Display 承接）。
3.3 给 Step 0.3 复现里的 `Input` 加 `Display: "/probe-skill "+s` → 跑该复现 → 预期 **PASS**。
```bash
gofmt -l pkg/run && CGO_ENABLED=1 go test -tags fts5 ./pkg/run -run 'SubagentDelivered|SubagentReceivesASteer|SubagentSkillSteer|Pause|Stop' -count=1
```
预期全 PASS。

### Step 3b —（L4）steer 用户行保留原行

3b.1 `pkg/llm/llm.go` `Message.DisplayText`（§3.1-P）；`pkg/state/message_sync.go storedContent`；`orchestration_llm.go steerMessage` 设 `DisplayText`。
3b.2 跑 Step 0.6 的复现 → 预期 **PASS**；在 `TestSkillSteerIsDeliveredBeforeItsSkillCard` 里补一条断言：第二次模型请求的最后一条消息 `DisplayText == "/probe-skill tighten it"`（给该测试的 `Input` 加 Display）且 `TextContent() == "tighten it"`。
3b.3 **整结构序列化点审计**（评审方已定位三处，逐一核实并写进验收记录；provider adapter 自建请求结构体、不整序列化 `llm.Message`）：
  - `pkg/state/runrt.go:185`（`MarshalWaitSessionSnapshot(messages)`）：JSON 往返——确认反序列化目标是 `[]llm.Message`（带 `json:"display_text,omitempty"` 会原样还原；即便不还原也只是丢 display，不影响模型上下文）。
  - `pkg/run/fork.go:294`：同上，fork 复制消息列表的 JSON 往返。
  - `pkg/run/config.go:894 planMessagePrefixHash`：对 `json.Marshal(msgs)` 取哈希。锚点前缀＝本轮**首次请求时**的历史长度（`stableReminderForRun` → `anchor`，`:668-700`），steer 消息都是之后追加的，永远在 `anchorLen` 之后 ⇒ `DisplayText` 不参与锚点哈希；同一 run 内 `session` 里的 steer 消息是同一个值，也不会让前缀漂移。记录结论即可，不改该函数。
  - `grep -rn "json.Marshal(" pkg/llm/<各 provider adapter> pkg/agent | grep -v _test` 核对没有把整条 `llm.Message` 直接塞进 provider 请求的代码；若发现 → STOP 上报。
```bash
gofmt -l pkg/llm pkg/state pkg/run && CGO_ENABLED=1 go test -tags fts5 ./pkg/llm ./pkg/state ./pkg/run -run 'AppendMessageSequence|Steer|Skill|Message' -count=1
```
预期全 PASS（含 `pkg/state` 全部 AppendMessageSequence 对齐/去重用例）。

### Step 4 — TUI

4.1 `run.go queueInput`（§3.2-F）、`handleActiveRunSlash`（§3.2-G）、`chat_session.go SendToSubagent`（§3.2-H）。
4.2 把 `chat_session_test.go:22414` 改写为 `TestActiveRunSteersExplicitSkillSlash`：同一 fixture（`fakeSession{steerAccepted: true, streamSlashReply: …}`），`handleActiveRunSlash(...)` 后断言：`preview.Steers == []string{"/review change"}`（原行）、`len(preview.FollowUp) == 0`、`len(preview.Rejected) == 0`；`q.Runtime().Snapshot()[0]` 的 `SkillName == "review"`、`SkillPath == "/skills/review/SKILL.md"`、`Display == "/review change"`、`Parts[0].Text == "change"`；`q.Recall()` 的 `Payload.(ComposerSubmission).SkillName == "review"`（召回仍完整）。
4.3 新增 `TestActiveRunStillQueuesUserShellFollowUp`（钉住保留的分支）：`state.onlyUserShellCommandsRunning()` 为真时（构造方式见 `chat_session_test.go:16360-16367` 一族）→ `preview.FollowUp` 为 1、`preview.Steers` 为空。
（§3.2-H 的两行字段拷贝由读 diff 覆盖：`fakeSession.SendToSubagent` 只记录 `ComposerSubmission`（`run_test.go:1351`），`run.Input` 在真实 `ChatSession.SendToSubagent` 里才构造，单测截获不到——评审已核，不另造测试。）
```bash
gofmt -l pkg/tui && CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -run 'ActiveRun|SkillCommand|SubagentView|QueuedSteer|DeliveredSteer' -count=1
```
预期全 PASS（含 `run_test.go` 的 `TestActiveRunSkillCommandRunsOnTheTextOfAFoldedPaste` :2765、`TestSubagentViewSkillCommandRunsOnTheTextOfAFoldedPaste` :2823，它们用 `Recall()` 不分车道，应保持绿）。

### Step 5 — Web 网关（技能同类 + L2/L3）

5.1 `run_control.go`：`input()`（I）、`withWebInputParts`（K）、`handleRunInput`（L）、`watchRunQueue`（N）、`releasedInput`（O）、`queuedInputReply`（Q）；`api_extra.go`：`skillCommandPrompt`（J）、`handleSubagentInput` / `subagentCommandInput`（M）、`handleSubagentQueuedInput` 的 `"message"`（Q）。
5.2 更新 `api_extra_test.go:6083 TestSubagentInputSendsARegisteredSkillCommand`：`Preview.PendingSteers == []string{"/probe-skill go"}`（原行），注释「the skill's expansion is what the subagent is sent」改为「the line as typed is what the queue previews; the expansion rides in the parts」。
5.3 新增 `TestSubagentInputSkillCommandKeepsTypedLineAndExpandsParts`（同 harness）：pkg/run 不导出子代理 channel 队列 runtime 的访问器，改用 `run.RecallSubagentInput(s.Runner, "sid", "agent-1")` 取回那条 steer（既有模式见 `subagent_test.go:6531` 一族），断言 `Parts[0].Text == "use probe-skill: go"`、`SkillName == "probe-skill"`、`Display == "/probe-skill go"`、`Text == "/probe-skill go"`；响应 `preview.pendingSteers` 同为原行。
5.4 新增 `TestRunInputSteersASkillCommandWithItsSelection`（照 `run_control_test.go:480` 的 fixture；用 `turn.ReplaceDynamicSource` 注册一条 `Category: turn.SkillCategory` 的动态命令，写法照 `api_extra_test.go:6088-6101`。`parseSlashCommandWithOptions` 走 `s.Core == nil → turn.ExecuteSlashCommand`；若 `&Server{}` 下它碰到 nil 依赖，改用 `newSubagentViewTestServer(t)`）：POST `{"message":"/probe-skill go"}` → 200 `accepted=true`、`preview.pending_steers == ["/probe-skill go"]`；`q.Runtime().Snapshot()[0]`：`SkillName == "probe-skill"`、`Parts[0].Text == "use probe-skill: go"`、`Display == "/probe-skill go"`。
5.5 新增 `TestRunInputLeavesBuiltinAndPlainLinesAlone`：`/model`（内置）与 `plain text` 两条 → `SkillName == ""`；`/model` 的 `Parts[0].Text == "/model"`（原文，保持现状），`plain text` 的 `Parts[0].Text == "plain text"`。
5.6 新增 `TestWatchRunQueuePublishesDeliveredSteerAsWritten`：构造 `Input{Text:"a  b", Display:"a\nb"}` 入队并经 runtime `DrainSteers`（触发 change hook）→ 订阅 `RunEvents`，`input_delivered` 的 `Text == "a\nb"`。
5.7 跑 Step 0.5 的 L2 复现 → 预期 **PASS**；新增 `TestReleasedAndRecalledInputsKeepTheTypedLine`：`releasedInput(Input{Text:"a b", Display:"a\nb"})` → `Text == "a\nb"`；`queuedInputReply(Input{Text:"a b", Display:"a\nb"}, true, …)["message"] == "a\nb"`；`handleSubagentQueuedInput` edit_last 的 `message` 同。
```bash
gofmt -l pkg/gateway && CGO_ENABLED=1 go test -tags fts5 ./pkg/gateway -run 'RunInput|SubagentInput|WatchRunQueue|PendingInput|Released|Recall' -count=1
```
预期全 PASS。

### Step 5c —（裁决 3）web `/` 选择器只列技能

5c.1 先 `peer_send` 告知 forebrain-harness-54 将改 `ForebrainPromptTextarea.vue` 一处（若对方正在改该文件，协调先后）。
5c.2 `ForebrainPromptTextarea.vue:383-386` 按 §3.3-R 改为只保留 `category === 'skill'`；`ForebrainPromptTextarea.test.ts` 里钉「句首 `/` 列出内置命令」的用例（grep `slashCommands` / `category` / `'/goal'` / `'/compact'`）改为断言只有技能，并补一条「句首 `/` 不列内置」。
```bash
cd frontend && pnpm typecheck && pnpm test -- ForebrainPromptTextarea
```
预期：typecheck exit 0、相关用例 PASS。

### Step 6 — 门禁

```bash
gofmt -l cmd pkg third_party plugins            # 无输出
CGO_ENABLED=1 go build -tags fts5 ./...          # ok
CGO_ENABLED=1 go vet -tags fts5 ./...            # 无输出
FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/llm ./pkg/state ./pkg/run ./pkg/gateway -count=1   # ok
FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -count=1                 # ok（~200s，timeout 调到 600000）
CGO_ENABLED=1 go test -tags fts5 ./pkg/architecture -count=1                                      # 仅预存红 TestTestFilesCorrespondToProductionFiles
cd frontend && pnpm typecheck && pnpm test && pnpm build                                          # 全绿
git status --short                                                                                  # 只有 §4 In scope 文件
```
`pkg/architecture` 出现新红（fan-out / 分层）→ STOP（本改动不新增任何跨包 import）。

### Step 7 — tmux 真机验收（必做；加载 `run-forebrain` 技能用 driver）

构建：`go build -tags fts5 -o ./build/bin/forebrain ./cmd/forebrain`，`$D build`（driver 自建到 `$TMPDIR/forebrain-run/forebrain`，改完 Go 代码必跑，否则跑的是旧二进制）。
探针技能：在 driver 的隔离 `FOREBRAIN_HOME` 下放 `skills/probe-skill/SKILL.md`（frontmatter `name: probe-skill` / `description: probe`，正文一行 `PROBE-SKILL-BODY`；`slash-command` 默认 true，`pkg/skill/activation.go:80-84`）。`$D stop && $D reset` 后再 `start`，让技能目录在启动前就位。

- **S1 主视图（问题 1）**：`FAKE_REPLY_DELAY=20 FAKE_DUMP_DIR=$TMPDIR/fb-dump $D start tool '{"name":"shell","arguments":{"command":"sleep 6"}}'`；`$D submit 'go'`；`$D wait '●' 25`；`$D submit '/probe-skill tighten it'`；`$D screen`。
  期望：预览标题 **`Messages to be submitted after next tool call`** 下有 `↳ /probe-skill tighten it`；**没有** `Queued follow-up inputs`。等 `sleep 6` 结束、延迟回复到达后再 `$D screen`：顺序 **you 卡 `/probe-skill tighten it` → `Skill probe-skill`（Loaded from …）→ 模型回答**；`grep -l 'The user selected the `probe-skill` skill' $TMPDIR/fb-dump/request-0002.json` 命中。
- **S2 子代理视图（问题 2）**：`ENABLE_SUBAGENT=1 $D start subagent-steer`；`$D submit 'dispatch'`；`$D wait 'general-purpose' 25`；Down Down Enter 进子代理视图；`$D submit '/probe-skill tighten it'`；`$D screen`（预览原行）；等工具边界（`sleep 8`）后 `$D screen`：**you 卡 `/probe-skill tighten it` 在 `Skill probe-skill` 卡之上**，之后才是子代理的回答 delta。
- **S3 回归（无技能 steer 不变）**：S1 同模式发一句普通 `also this` → 仍是 steer 预览、you 卡在回答前；无 Skill 卡。
- **S4 退出自救**：运行中 Ctrl+C 两次 → `EXIT_CODE=0`；再 `start` 可恢复。
- **S6（L4）重放**：S1 结束后 `$D db "SELECT content FROM fb_messages WHERE role='user' AND parts NOT LIKE '%is_meta%' ORDER BY id DESC LIMIT 1"` == `/probe-skill tighten it`，且 `SELECT count(*) FROM fb_messages WHERE role='user' AND parts NOT LIKE '%is_meta%'` == 2（`go` 与 steer；meta 的 activation 行不计——列名是 **`parts`**（`schema_migrations.go:444`），`$D messages` 是否过滤 meta 未核实，所以用 SQL 按 parts JSON 过滤）；`$D stop` 后用同一 `FOREBRAIN_HOME` `start`（或 `/resume` 该会话）→ `$D screen`：`you /probe-skill tighten it` 在 `Skill probe-skill` 卡之上、`Skill` 卡在回答之上（`skillEventAnchorPosition` 落在 steer 行之后）。
- **S5 Web（REST）**：`FOREBRAIN_HOME=<隔离home> FOREBRAIN_GATEWAY_TOKEN=<token> ./build/bin/forebrain gateway start`（后台，process 工具），起一个会卡住的运行，`POST /runs/<runId>/input {"message":"/probe-skill go"}` → `preview.steers` 含原行；等 `input_delivered` → `text == "/probe-skill go"`；`POST .../subagents/<agent>/input` 同理；多行 `"a\nb"` 的 steer 请求体 dump 中换行保留。若 Web 真机因 provider 环境不可达，以 Step 5 的 Go 测试为证并在验收记录写明。
- 全程隔离 `FOREBRAIN_HOME`，不碰真实 `~/.forebrain*`；结束 `tmux kill-session`、删临时目录。

---

## 6. Done criteria（机器可查）

1. Step 0.2/0.3/0.5/0.6 的四个新测试在基线上 FAIL（0.6 为编译失败）、修复后 PASS（输出各留一份）；Step 2.4 `TestSkillSteerRowIsWrittenBeforeItsSkillCard` PASS（`delivered` < `flush` < `step:skill`）。
2. `TestActiveRunQueuesExplicitSkillSlashInsteadOfSteering` 不再存在；`TestActiveRunSteersExplicitSkillSlash` PASS。
3. Step 6 的 gofmt / build / vet / 五包 Go 测试 / 前端 typecheck+test+build 全绿，`pkg/architecture` 仅预存红。
4. `git status --short` 只含 §4 In scope 文件；`git diff --stat -- frontend/` 只有 `ForebrainPromptTextarea.vue` 与其测试。
5. `grep -c 'submission.SkillPath) != "" || state.onlyUserShellCommandsRunning()' pkg/tui/run.go` = 0；`grep -c 'slashAtStart.value ? records' frontend/src/components/ForebrainPromptTextarea.vue` = 0。
6. S1/S2/S6 取证：主视图 steer 预览、you 卡在 Skill 卡之上、原行含 `/probe-skill`；DB user 行 content 为原行且重放顺序 you → Skill → 回答、user 行无重复。
7. `plans/392-skill-command-steer-both-views.md` 落盘、状态 DONE＋验收记录；`plans/README.md` 索引行同步；本计划两轮评审留下的 worktree `.worktrees/plan-review-491493599/`、`.worktrees/plan-review-2121862669/`（detached `1d6726b6`，含评审探针测试）在实施开始前 `git worktree remove --force` 清掉（其余 `plan-review-*` 属别的会话，不碰），`git worktree list` 不再有这两个。

---

## 7. STOP conditions

- Step 0 任何 grep 计数 ≠ 1，或 0.2/0.3/0.5/0.6 在基线上 PASS、0.4 FAIL → 根因链与现场不符，停报。
- Step 3b.3 发现整条 `llm.Message` 被直接序列化进 provider 请求的代码点，或 `pkg/state` 任一对齐/去重用例变红，或 Step 2.4 的 `"flush"` 不在 `"step:skill"` 之前 → L4 的持久化方案需重新评估，停报（不带着重复行/错序风险硬上）。
- 需要改 web 运行中内置 slash 命令行为、`/init`／`/compact`／`/goal` 的 webchat 暴露、前端 `slash_reply`/picker 流、`pkg/event` payload 结构、`SteerDelivery` 首 token 提交契约、`cleanInputText` 折叠语义 → 超出 scope（owner 终裁），停报。
- Step 2 后 `TestSteerWhoseSkillFailsToLoadIsStillDelivered` 或任何 Rollback/Retract 测试变红且原因不是本改动 → 停报。
- 真机 S1/S2 仍出现 Skill 卡在 you 卡之前 → 先查两条事件路径（`InputQueueChangedMsg` 直入 UI 队列 vs step 事件经总线）是否乱序，**不得**加 sleep/延迟之类的表面补丁。
- Step 6 `pkg/architecture` 出现新红；`pkg/tui` 全量出现与本改动无关的红（另两个会话在同一树工作）→ 记录并报告对应 agent，不自行修。
- forebrain-harness-54 正在改 `ForebrainPromptTextarea.vue` 且无法协调 → Step 5c 单独报告，不抢改。

---

## 8. Maintenance notes

- 审阅重点：(1) `publishSteerOnCommit` 是否只在 `Commit` 路径运行、`Rollback` 是否绝不运行，且其中「先 flush 再发卡」的顺序不被后续改动颠倒（重放锚点 `skillEventAnchorPosition` 依赖用户行早于事件落库）；(2) 5 处 `TurnInputEntry` 逐字段拷贝是否都带上了 `Display`（漏一处就会在 `DrainAll`/`Snapshot` 丢字段）；(3) `storedContent` 只对 user 行读 `DisplayText`，且 PartsJSON 仍由 Parts 生成——对齐靠 `ParseMessage` 优先 partsJSON，改动这条优先级任何一处都会让 `AppendMessageSequence` 重复追加。
- 以后给 `TurnInputEntry` 加字段：5 处拷贝点要同步（grep `SkillPath: entry.SkillPath` / `SkillPath: in.SkillPath` 定位）。
- `SubagentInputDeliveredPayload.Text` / `InputDeliveredPayload.Text` 语义现为「发送者写的原行」；peer 信封仍由 `Describe` 派生 `PeerBody`。
- `llm.Message.DisplayText` 与 `Origin`/`ReasoningModel` 同类：持久化元数据，adapter 不得读；新增 adapter 或整序列化点时要维持。
- 后续可能的方向（不在本期，owner 终裁）：web 主视图运行中的内置 slash 命令与前端 slash_reply/picker 流；`/init`／`/compact`／`/goal` 的 webchat 暴露。

## 9. 计划评审记录

- 2026-11-26 独立冷读（plan 子代理）：无 BLOCKER；锚点逐条核对（唯一漂移：`TestSubagentReceivesASteerAtItsNextToolBoundary` 在 :5919；`/skills` 产出点在 `chat_slash.go:1917-1923` 而非 `commands.go:2341`，已改）。REVISE 四条已折入：Step 0.2 断言按 `ToolName=="skill"` 过滤；Step 5.3 改用 `run.RecallSubagentInput`；删去截获不到 `run.Input` 的 4.4；Step 2.4 写明「重投递 = 第二次 `Execute`」并指明现成范本。NIT 已折入：`skillCommandPrompt` 非技能路径返回空串；§3.3-L 拆成两条语句；`OnCommit` 注释列出 `Commit` 全部 5 类入口；矩阵补 #9b。
- 2026-11-26 owner 两轮裁决后扩 scope：并入 L2/L3/L4（Step 0.5/0.6、3b、5.7、S6）；L1 保持现状（§4 Out of scope 首条、矩阵 #15）；`/` 选择器只列技能（Step 5c、矩阵 #21）。
- 2026-11-26 第二轮独立评审（custom/qwen4-max-preview，在隔离 worktree `.worktrees/plan-review-491493599/` 实施了 A/B/C/D/E/P/K/L 并独立取红转绿；主树零改动）：approve with changes。已折入：① Step 0.1 第 6 条 drift grep 在基线计数=2（`storedContent` 与 `messageText` 同文）→ 改锚函数签名；② Step 0.3 复现引用尚未存在的 `Input.Display` 会让整包编译红、连 0.2 一起拖红 → 基线复现不带 Display，Step 3 再加，并规定 Step 0 只有 0.6 允许编译红；③ 原 3b.3 的 grep 证明不了结论 → 改为审计三处真实整序列化点（`runrt.go:185`、`fork.go:294`、`config.go:894`）＋provider adapter 核对，并说明 `planMessagePrefixHash` 的锚点前缀早于任何 steer、`DisplayText` 不参与；④ 重放顺序：评审指出 Skill 事件先于用户行落库、顺序修复前后不变——进一步核实 `skillEventAnchorPosition`（`commands.go:1462-1494`）会把技能 steer 的 Skill 卡锚到**本轮最初的 user 行**之后（错位），故 §3.1-D 增加「提交时先 flush 转录再发卡」（`publishSteerOnCommit`）＋ Step 2.4 顺序测试 ＋ S6 重放断言。轻微证据瑕疵已改：§2.1「整行送模型」→「展开后的请求、技能不激活」；0.5「Parts 为 nil」→「回落折叠文本」；Step 4 测试文件名 `run_test.go`；§1 注明预览空白折叠。评审方同类排查结论（子代理释放输入复用 `releasedInput`、`mergeSteers` 读 `Parts[0]`、follow-up 经 `req.input()`、其余 SkillName 生产者均为空闲派发）与本计划一致。
- 2026-11-26 第三轮独立评审（同模型，新 worktree `.worktrees/plan-review-2121862669/` 实施修订版 A/B/C/D/E/P/K/L：`pkg/run` 全量 ok 45.6s，新增 `TestReviewSkillSteerRowWrittenBeforeSkillCard`／`TestReviewSkillSteerRollbackWritesNoRowAndNoCard` 实证 `delivered < flush < step:skill`、回滚不落行不发卡）：approve with changes。已折入：① Step 2.4/S6 的「user 行恰 2 条」改为「非 meta user 行恰 2 条」（技能 activation 是 `IsMeta` user 行、steer 路径 `Ephemeral=false`，会落库，总数 3），S6 改用按 `parts_json` 过滤 `is_meta` 的 SQL；② §3.1-D `steerMessages` 删去未使用的 `step` 声明（否则 Go 编译错）；③ §3.1-D 补「时延权衡」（首 token 关键路径上一次同步 DB 事务）并写进矩阵 #9b。
- 2026-11-26 第四轮独立评审（同模型，复用 `.worktrees/plan-review-2121862669/` 重跑四个验证测试 ok）：approve with changes。已折入：① S6 SQL 列名 `parts_json` → **`parts`**（`schema_migrations.go:444`，前者是 Go 字段名误植，执行必报 `no such column`）；② Step 2.4 补 fixture 前置：`fb_messages.run_id` 外键 `fb_runs(id)`，须先 `Ensure` 会话＋`CreateRun` 并以同一 runID 入 ctx，否则 flush 只记 WARN、行数断言得 0。
- 2026-11-26 第五轮独立评审：approve（无剩余实质问题）。

## Implementation Status

**VERDICT: APPROVE**（2026-11-27，tech lead 复核）

- **落地**：owner 指令「合并回主树dev分支」→ 主树 `git merge --ff-only skill-command-steer-both-views`，dev `1d6726b6..29773e4a`（未 push；合并前核对 dev 未漂移、可 ff、主树无他人未提交改动，两位并行 agent 均确认无交集）。落 dev 门禁在主树复跑：gofmt 空／`go build -tags fts5` ok／`go vet` ok／`pkg/llm` ok 188.4s、`pkg/state` ok 6.4s、`pkg/run` ok 46.2s、`pkg/gateway` ok 12.5s、`pkg/tui` ok 210.7s／`pkg/architecture` 仅预存红／前端 typecheck 0、195 文件 1602 用例全绿、build ok。worktree `.worktrees/skill-command-steer-both-views` 与分支随后按 owner 指令「删除」清理：删前核实 worktree 干净（`status --porcelain` 空）、分支已完全并入 dev（`git branch --merged dev` 含它），`git worktree remove` ＋ `git branch -d skill-command-steer-both-views`（was `29773e4a`）。

- **执行器**：`alibaba/deepseek-v4.1-flash-1017`；worktree `.worktrees/skill-command-steer-both-views`，分支 `skill-command-steer-both-views`（基线 dev `1d6726b6`），3 提交未 push：`46c9d3c3` fix(run) 引擎、`8fd2b013` fix(tui)、`29773e4a` fix(gateway)+前端。19 文件 +946/−84，全部在 §4 In scope 内；工作区干净。
- **tech lead 亲跑（不采信执行器报告）**：
  - 基线取红（`go test -overlay` 把生产文件换回 `1d6726b6`，保留新测试）：0.2 `order = [step:skill:tool_call_started step:skill:tool_call_completed delivered]`；0.3 `Text:adjust approach`；0.5 `first second`；0.6 编译红（`Message has no field DisplayText`）。修复后四个全 PASS，Step 2.4 `delivered < flush(user) < step:skill` PASS，回滚用例 PASS。
  - 门禁：gofmt 无输出；`go build -tags fts5 ./...` ok；`go vet` ok；`pkg/llm` ok 170.9s、`pkg/state` ok 8.9s、`pkg/run` ok 46.8s、`pkg/gateway` ok 13.0s、`pkg/tui` ok 210.4s；`pkg/architecture` 仅预存红 `TestTestFilesCorrespondToProductionFiles`（`pkg/tui/deliverables_outcome_test.go`）；前端 typecheck 0、195 文件／1602 用例全绿、build ok。Done 5 两条 grep = 0/0；旧测试 0 处、新测试 1 处。
  - 真机（worktree 自建二进制，隔离 `FOREBRAIN_RUN_WORK`／tmux `fb-review-steer`／端口 8773，已清理，`~/.forebrain` mtime 未变）：
    - **S1 主视图**：运行中 Enter `/probe-skill tighten it` → 预览落在 **`Messages to be submitted after next tool call`**（无 `Queued follow-up inputs`）；送达后转录 `› /probe-skill tighten it` → `● Skill probe-skill（Loaded from …/.versions/probe-skill/v1/SKILL.md）` → `● done`；`request-0003.json` 含 ``The user selected the `probe-skill` skill``。
    - **S2 子代理视图**：预览同在 steer 段；转录 `› answer briefly` → `● Ran sleep 8` → `› /probe-skill tighten it` → `● Skill probe-skill` → `● steer received, still working`；worker 转录 user 行 content = `/probe-skill tighten it`。
    - **S6 重放**：`SELECT content … role='user' AND parts NOT LIKE '%is_meta%'` → `/probe-skill tighten it`，count = 2；`resume` 回放顺序与 live 完全一致、无重复行。
- **执行器偏离（已审，接受）**：① 引擎 step 事件 Kind 常量是 `tool_call_started/completed`（计划写 `tool_started` 为示意）；② meta 标记为 PartsJSON 的 `"type":"is_meta"`（S6 SQL 一致）；③ 复用既有 `failAfterToolCallLLM`/`errProviderFailed`；④ `orderedStepLog` 助手放 `subagent_test.go` 同包共用；⑤ Step 2.4 取「Skill 卡之前最后一次含 user 的 flush」——tech lead 推演确认该断言对「先发卡后落库」的错误实现同样取红；⑥ 5c 顺带删除已成死状态的 `slashAtStart`（含 watch 依赖）并调整两条测试 fixture 的 category；⑦ 按 hand-off 跳过 `plans/392` 落盘、索引行、评审 worktree 清理（已由 tech lead 完成）与 5c.1 `peer_send`。
- **未做／限制**：S5 Web REST 真机未跑（本机无 WS 客户端起不了 active run），按计划回落为 Step 5 handler 级 Go 测试为证；owner 若要 REST 真机复核需在能跑 WS 客户端的环境补做。S4 顺带观察（预存，不属本改动）：运行中第一次 Ctrl+C 只取消运行，退出需再连按两次（EXIT=0）。S2 环境坑：复用旧 home 时恢复的会话带旧配置会报 `subagent_run` unknown tool，须从新 home 起。
