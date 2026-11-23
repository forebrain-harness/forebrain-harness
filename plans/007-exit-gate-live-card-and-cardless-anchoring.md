# Plan 007: exit_plan_mode 审批期间 TUI 不再画假 "Exited plan mode" 卡、取消后不再多一张 "Canceled" 卡；无卡 gate 的确认行与 review 卡按真实顺序落位（live 与回放一致）

> **执行者须知**：按步骤顺序执行，每步跑验证命令并确认预期结果后再进下一步。触发
> "STOP conditions" 时停下报告，不要即兴发挥。完成后更新 `plans/README.md` 里本计划的状态行。
> **禁止提交代码**（owner 手动提交）：不建分支、不 commit、不 push，改动留在工作区。
>
> **Drift check（先跑）**：
> `git diff --stat 9656051 -- pkg/tui/reducer.go pkg/tui/render.go pkg/tui/channels.go pkg/tui/commands.go pkg/tui/tty_session.go pkg/tui/notify.go pkg/tui/reducer_test.go pkg/tui/commands_test.go pkg/tui/chat_session_test.go`
> 写计划时工作区里 `pkg/tui/notify.go`、`pkg/tui/notify_test.go` 已有 owner 未提交的改动
> （`publishRunEvent` 顶部新增 `markEventShownOnce`，与本计划无关）——**不要还原、不要整理它们**。
> 其他文件若出现差异，对照下方 "Current state" 摘录逐段核对，不一致即 STOP。

## Status

- **Priority**: P1（owner 报告的两个可见 bug，每次 plan 模式退出都会遇到）
- **Effort**: M
- **Risk**: MED（改 TUI 唯一的 live 工具帧入口与审批确认行的落位规则；有充分测试面）
- **Depends on**: none
- **Category**: bug
- **Planned at**: commit `9656051`，2026-10-10（工作区有 owner 未提交改动，见上）

## Why this matters

owner 报告（附两张 TUI 截图）：

1. 用户在 exit_plan_mode 审批里选"让其他模型评审计划"后，plan-reviewer 还在跑、仍处于 plan 模式，
   transcript 里却出现 **"◆ Exited plan mode"**——一个从未发生的退出。
2. 用户在 exit_plan_mode 审批上按 Ctrl+C 取消后，已经有 "✗ You canceled forebrain's request to exit plan mode"，
   下面又多出一张 **"◆ Canceled"** 卡。

两者同一个根因：**主 agent 的工具步骤在 TUI 里走一条没有 gate 过滤的 live 路径**，把 exit gate 的
等待帧（running / awaiting approval）画成了卡。owner 的常设裁决是"exit_plan_mode 审批 gate 的等待态
在任何 surface 都不画卡——审批 overlay 就是全部等待；只保留三类 settled 卡：用户 deny 的
'Kept planning'、失败、批准后的 'Exited plan mode'"。之前两次修复（d501e09、08a3145）只堵了
已发布事件漏斗和回放，漏了主 agent 这条路径，所以 bug 仍在。

去掉这张假卡后会暴露第二个问题：审批确认行（以及 review 卡）的落位规则是"插到 transcript 里最后一张
工具卡之前"，这条规则依赖 gate 自己有一张停靠卡。exit gate 没卡时，确认行会跳到**上一张无关工具卡**
（如 "◆ Entered plan mode"、写计划文件的卡）之前。回放路径**今天就已经这样错位**（真机已复现，见下）。
所以本计划必须同时修落位规则，live 与回放一并修，保证 1:1。

## 根因与证据（全部经真机复现）

### 复现（`.claude/skills/run-forebrain/driver.sh`，`toolcall` 模式，HEAD 9656051 + 工作区）

```
$D start toolcall; $D submit 'plan something'; $D key Enter   # 批准 enter_plan_mode
# exit gate 停靠时（审批 overlay 打开）屏幕上已经是：
● calling exit_plan_mode

◆ Exited plan mode          ← bug 1：等待中的 gate 帧被画成卡，标题落进 else 分支
─────────────────────── Tool approval / Tool: exit_plan_mode …

$D key C-c                    # 取消审批后：
✗ You canceled forebrain's request to exit plan mode

◆ Canceled                    ← bug 2：同一张等待卡被 FinalizePendingTools 改成 canceled
```

选 "4. Ask another model to review this plan" 时（driver 里 review 因无计划文件失败）屏幕为
`✔ You asked … to review the plan` → `● Failed to start 1 plan-reviewer task` → `◆ Exited plan mode` → overlay 重新提示，
与 owner 截图 1 的形态完全一致（确认行和 review 卡被插到假卡之上）。

重启回放同一个取消会话（`RESUME_ID=<sid> $D start reply`），**今天已经错位**：

```
● calling enter_plan_mode
✔ You approved forebrain to enter plan mode
✗ You canceled forebrain's request to exit plan mode     ← 跑到了 Entered plan mode 卡之上
◆ Entered plan mode
● calling exit_plan_mode
```

### 证据链（file:line 为写计划时的工作区）

1. **漏网的生产者**：主 agent 的工具步骤由 `runAuditStepHook` 发出（`pkg/tui/chat_turn.go:233-293`）。
   `agentID == ""` 分支对 ToolStarted/ToolCompleted 先 `persistRunEvent`（只落库，不画），再直接调
   `s.notifyToolStepHooks(...)`（`chat_turn.go:293`）。`notifyToolStepHooks`（`pkg/tui/notify.go:436-542`）
   只检查 `tool.ToolStepUserVisible`，**没有** `tool.ToolStepHoldsNoCard`，于是：
   - started 事件 → `NewMessageMsg{MsgKindTool, ToolName:"exit_plan_mode", ToolMeta.Status:"running"}`；
   - 带 ActionID 的 completed 事件 → 同上，`Status:"awaiting approval"`（`pkg/tool/format.go:2295-2296`）。
2. **d501e09 的过滤只在另一条路径上**：`pkg/tui/notify.go:797-812`（`publishRunEvent` 的事件漏斗）有
   `!tool.ToolStepHoldsNoCard(...)`，但主 agent 的工具步骤不走这里（`persistRunEvent` 不经漏斗）。
   之前的计划（`docs/plan/EXIT_PLAN_GATE_GHOST_CARD_PLAN.md`）写的"TUI live 无 canceled 生产者，已干净"不成立。
3. **reducer 不过滤**：`pkg/tui/reducer.go:1893` 的 `case MsgKindTool` 对任意工具消息都 `buildFrame(FrameTool, …)`。
4. **bug 1 的标题**：`pkg/tui/render.go:2653-2663`，d501e09 删除了 running 标题（以为不会有帧到达），
   running / awaiting approval 帧落进 else → **"Exited plan mode"**。
5. **bug 2 的标题**：取消时 `abortPendingApproval`（`pkg/tui/chat_turn.go:1095` 起）发 `StreamResetMsg`，
   run 循环随即 `renderer.FinalizePendingTools()`（`pkg/tui/run.go:339-345`），后者把所有 pending 工具块改成
   `canceled`（`pkg/tui/render.go:782-793`）；通用覆盖 `render.go:2811-2814` 把它标成 **"Canceled"**。
6. **落位规则依赖停靠卡**：
   - live：`Renderer.PrintApprovalConfirmation`（`render.go:739-757`）恒置 `InsertBeforeLastTool: true`；
     `retainFrameLocked`（`reducer.go:4987-4992`）据此 `insertBeforeLast(… prev.Kind == FrameTool …)`——
     从尾部往回找**任意**最后一张工具卡。plan review 卡同理：`emitFanoutFrame` 置
     `InsertBeforeLastTool: fs.Verb == "review"`（`reducer.go:841-845`），`retainFrameLocked` 用
     `replaceOrInsertBeforeLastTool`（`reducer.go:4975-4980`，实现在 `tty_session.go:1223-1239`）。
   - 回放：`approvalEventAnchorPosition`（`pkg/tui/commands.go:512-541`）把审批记录锚在**结果行之后**，
     其 doc 原话："a confirmation is inserted above the last tool block already in the view … so it has to be
     flushed after its own card exists **or it lands above some earlier, unrelated one**"——exit gate 的
     canceled 行 / review 交付行被丢弃、没有卡，正好落进这句话描述的错位。
     `replayApprovalEventFrames`（`commands.go:618-655`）同样恒置 `InsertBeforeLastTool: true`。
7. **识别 exit gate 的数据都在**：live 的 `turn.ToolApprovalRequest.ActionKind`、回放的
   `event.ApprovalResolvedPayload.ActionKind` 对 exit gate 都是 `"exit_plan_mode"`（状态库实证：
   `approval_resolved` 行 payload `{"action_kind":"exit_plan_mode","decision":"cancelled","tool_step_id":"call_fake_1",…}`）。

## 修复设计（一句话）

**TUI 里所有 live/回放工具消息都经过 `Reducer.reduceMessage` 的 `case MsgKindTool`——在这一个入口应用
`tool.ToolStepHoldsNoCard`（封存已流出的文本、不画卡）；凡是关于"无卡 gate"的记录（确认行、它要求的
plan review 卡）不再"插到最后一张工具卡之前"，而是按产生顺序追加；回放把这类记录锚在发出调用的
assistant 行之后、结果行之前。**

### 行为变化矩阵（TUI）

| 场景 | 旧（live） | 新（live） | 回放（新） |
|---|---|---|---|
| exit gate 停靠，overlay 打开 | "◆ Exited plan mode" 假卡 | 无卡；`● calling exit_plan_mode` 后直接是 overlay | — |
| 选 review（成功或失败） | 确认行、review 卡插在假卡之上，假卡悬挂 | `● calling exit_plan_mode` → `✔ You asked …` → review 卡，按顺序追加 | 同 live |
| review 交付后模型修订并再次 exit | 假卡悬挂到 run 结束 | 修订内容、新 gate 按顺序继续 | 同 live |
| Ctrl+C / Esc 取消 gate | 确认行 + "◆ Canceled" | 只有 `✗ You canceled …`，紧跟 `● calling exit_plan_mode` | 同 live（今天错位到 Entered plan mode 之上，修好） |
| 批准 | `✔ You approved …` + "◆ Exited plan mode" | 不变（确认行追加，随后 completed 卡追加） | 不变 |
| 拒绝（带反馈） | `✗ You did not approve …` + "◆ Kept planning └ 反馈" | 不变 | 不变 |
| 其他工具（shell、enter_plan_mode、request_permissions…）的审批 | 确认行插在被审批的卡之上 | **不变** | **不变** |

## Current state（摘录）

`pkg/tool/format.go:2264-2280`（共享谓词，**不改**）：

```go
// ToolStepHoldsNoCard reports whether a tool step's transcript card is
// withheld: the exit-plan gate's wait lives entirely in its approval prompt,
// so no surface paints a card for it while the call runs, awaits the
// decision, or was abandoned to that wait — waiting, abandoned and canceled
// hold no card. Only the settled answers draw: the user's denial, a failure,
// the exit itself.
func ToolStepHoldsNoCard(toolName, status string) bool {
	if !strings.EqualFold(strings.TrimSpace(toolName), "exit_plan_mode") {
		return false
	}
	switch strings.TrimSpace(status) {
	case "running", "awaiting approval", "canceled":
		return true
	default:
		return false
	}
}
```

`pkg/tui/reducer.go:1893-1896`（工具消息唯一入口；`reducer.go` 已 import `pkg/tool`）：

```go
	case MsgKindTool:
		stepID := strings.TrimSpace(msg.StepID)
		outputKey := liveToolOutputKey(msg)
		if msg.ToolOutputDelta {
```

同文件的缓冲封存工具（`reducer.go:2052-2068`）：`flushBufferedText(buf, at...) []Frame` 把已流出的
reasoning/assistant 文本封存成 final 帧；`withFlushedBuffers(buf, next, at...)` = 封存 + 追加 `next`。
case 末尾的普通路径是 `buf := r.agentBuf(agentID); return r.withFlushedBuffers(buf, r.buildFrame(FrameTool, toolName, msg), msg.Timestamp)`。

`pkg/tui/notify.go:797-812`（d501e09 加的漏斗过滤，引入 reducer 入口过滤后成为重复检查）：

```go
	case event.RunEventToolStarted:
		var p event.ToolCallStartedPayload
		// The exit-plan gate's wait paints no card on any surface: its
		// approval prompt is the whole wait (tool.ToolStepHoldsNoCard).
		if json.Unmarshal(evt.Payload, &p) == nil && !tool.ToolStepHoldsNoCard(p.ToolName, "running") {
			s.notifyUIForSession(evt.SessionID, subagentToolStepMsg(evt, p.StepID, p.ToolName, firstNonEmpty(p.Summary, p.Description), p.ToolMeta, evt.Type))
		}
	case event.RunEventToolCompleted:
		var p event.ToolCallCompletedPayload
		if json.Unmarshal(evt.Payload, &p) == nil && !tool.ToolStepHoldsNoCard(p.ToolName, p.ToolMeta.Status) {
			msg := subagentToolStepMsg(...)
			...
```

`pkg/tui/render.go:739-757`：

```go
func (r *Renderer) PrintApprovalConfirmation(agentID, text string, maxLines int) {
	...
	r.appendFrameViewportLocked(Frame{
		Kind:                 FrameStatus,
		Content:              strings.TrimSpace(sgrPattern.ReplaceAllString(text, "")),
		MaxDisplayLines:      maxLines,
		InsertBeforeLastTool: true,
		AgentID:              strings.TrimSpace(agentID),
		Final:                true,
	})
}
```

唯一生产调用方 `pkg/tui/channels.go:1044`（`ApprovalSink.printApprovalConfirmation(req, decision)` 内）：

```go
		s.renderer.PrintApprovalConfirmation(req.AgentID, symbol+" "+msg, approvalConfirmationMaxLines)
```

测试调用方：`pkg/tui/chat_session_test.go:17961`、`:18020`。

`pkg/tui/reducer.go:838-846`（`emitFanoutFrame`）：

```go
	return Frame{
		Kind:   FrameFanout,
		StepID: fs.StepID,
		// A plan review is part of the approval exchange that asked for it:
		// it lands above the parked exit-plan call, between that approval's
		// two confirmation lines, not at the transcript tail. Every other
		// fanout card keeps its producer order.
		InsertBeforeLastTool: fs.Verb == "review",
		RunID:                r.activeRunID,
```

（plan review 只可能来自 exit gate：`completeSurfacePlanReviewRequest`，`pkg/tui/chat_surface.go:835-850`，
对非 `exit_plan_mode` 的 action 直接报错。）

`pkg/tui/reducer.go:4974-4996`（`Renderer.retainFrameLocked` 内）：

```go
	if f.Kind == FrameFanout || f.Kind == FrameTool || f.Kind == FrameMemoryCompact || f.Kind == FrameSkillInstall {
		if f.Kind == FrameFanout && f.InsertBeforeLastTool {
			// A plan-review card belongs to the approval exchange ...
			r.vm.replaceOrInsertBeforeLastTool(f)
		} else {
			r.vm.replaceOrAppendBlock(f)
		}
		...
	} else if f.Kind == FrameStatus && f.InsertBeforeLastTool {
		// Approval confirmations explicitly land above the tool block they
		// authorise, so "✔ You approved" always reads before "● shell ran".
		r.vm.insertBeforeLast(func(prev Frame) bool {
			return prev.Kind == FrameTool
		}, f)
	} else {
		// Ordinary statuses retain producer order. ...
		r.vm.append(f)
	}
```

`replaceOrInsertBeforeLastTool` 定义于 `pkg/tui/tty_session.go:1223-1239`，唯一调用方是上面 `reducer.go:4980`。
`Frame.InsertBeforeLastTool` 的 doc 在 `reducer.go:73-78`（提到 "or the plan-review card the confirmation asked for"）。

`pkg/tui/commands.go:528-541`：

```go
func approvalEventAnchorPosition(evt event.RunEvent, callRow, resultRow map[string]int) int {
	stepID := approvalEventToolStepID(evt)
	if stepID == "" {
		return -1
	}
	if row, ok := resultRow[stepID]; ok {
		return row + 1
	}
	row, ok := callRow[stepID]
	if !ok {
		return -1
	}
	return row + 1
}
```

（回放语义：`pending[pos]` 在 `turns[pos]` 回放**之前** flush——`commands.go:395-397` 的 `flush(i); replayTurnWithReducer(…turns[i]…)`。
`callRow[id]` 是发出该调用的 assistant 行下标，`resultRow[id]` 是答复它的 tool 行下标。
`planReviewAnchorByAction`（`commands.go:566-588`）复用 `approvalEventAnchorPosition`，所以 review 事件会跟着一起改锚点。）

`pkg/tui/commands.go:618-655` `replayApprovalEventFrames` 的返回值：

```go
	return []Frame{{
		Kind:                 FrameStatus,
		Content:              line,
		MaxDisplayLines:      approvalConfirmationMaxLines,
		InsertBeforeLastTool: true,
		AgentID:              strings.TrimSpace(p.AgentID),
		RunID:                evt.RunID,
		Final:                true,
	}}
```

### 仓库约定

- 注释写"为什么"，英文、完整句子，风格见上面摘录；不写"修复了 bug X"这类历史叙述。
- 被删掉的语义不留反向断言（"删掉的语义无痕"——owner 既有裁决）：旧测试按新契约整体改写，不新增
  "断言某卡不再出现"之外的考古式用例。
- TUI 不得 import 核心运行时内部包（`pkg/architecture` 测试守护）；本计划不增删任何 import。

## Commands you will need

| 用途 | 命令 | 成功预期 |
|---|---|---|
| 编译 | `CGO_ENABLED=1 go build ./...` | exit 0 |
| TUI 测试 | `CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -count=1` | ok |
| 单测过滤 | `CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -run '<Name>' -count=1` | ok |
| 全量 | `CGO_ENABLED=1 go test -tags fts5 ./pkg/... ./cmd/forebrain -count=1` | ok（`pkg/gateway` 偶发 macOS TempDir 清理 flake，单独 `-count=3` 复跑确认） |
| vet | `go vet ./...` | 无输出 |
| fmt | `gofmt -l pkg cmd` | 无输出 |

写计划时基线：`CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -run 'TestPlanReviewCardLandsBetweenTheApprovalConfirmations|TestReplayTimelinePutsThePlanReviewCardBetweenTheApprovalLines|TestReplayDrawsNoCardForCanceledExitGate|TestReplayDropsCanceledExitGateSettledRow|TestRendererViewportPrintApprovalConfirmation|TestRendererViewportApprovalConfirmationLimitedToThreeLines|TestNotifyToolStepHooks' -count=1` → ok；`gofmt -l pkg cmd` 无输出。

## Suggested executor toolkit

- 真机验收用 `run-forebrain` skill（`.claude/skills/run-forebrain/SKILL.md`）。**必须**用独立目录/会话/端口，
  不要和别人正在用的 driver 撞车：
  `export FOREBRAIN_RUN_WORK=$TMPDIR/fb-007 FOREBRAIN_RUN_SESSION=fb-007 FOREBRAIN_RUN_PORT=8807`。
  改完 Go 代码后先 `$D build` 再 `$D start`（start 会复用旧二进制）。

## Scope

**In scope**（只改这些文件）：
- `pkg/tui/reducer.go` — `reduceMessage` 的 `case MsgKindTool` 入口过滤；`emitFanoutFrame` 去掉 review 的
  `InsertBeforeLastTool`；`retainFrameLocked` 删掉 FrameFanout 的 InsertBeforeLastTool 分支；`Frame.InsertBeforeLastTool` doc。
- `pkg/tui/tty_session.go` — 删除变成死代码的 `replaceOrInsertBeforeLastTool`。
- `pkg/tui/render.go` — 新增 `approvalGateHoldsNoCard`；`PrintApprovalConfirmation` 增加落位参数；更新 exit_plan_mode 标题表注释。
- `pkg/tui/channels.go` — 调用处传入落位参数。
- `pkg/tui/commands.go` — `approvalEventAnchorPosition` 与 `replayApprovalEventFrames` 对无卡 gate 的规则；相关 doc 注释。
- `pkg/tui/notify.go` — **只**删 `publishRunEvent` 里 `RunEventToolStarted`/`RunEventToolCompleted` 两处 `ToolStepHoldsNoCard` 条件与其注释（`:799-801`、`:806`）。
- 测试：`pkg/tui/reducer_test.go`、`pkg/tui/commands_test.go`、`pkg/tui/chat_session_test.go`。
- 文档勘误：`docs/plan/EXIT_PLAN_GATE_GHOST_CARD_PLAN.md`、`docs/plan/PLAN_REVIEW_DELIVERY_SILENT_PLAN.md`（各加一段勘误引用，见 Step 8）。

**Out of scope**（不要碰）：
- `pkg/tool/format.go` 的 `ToolStepHoldsNoCard` 语义与 `pkg/tui/render.go:2653-2663` 标题表逻辑（只改注释）——
  过滤之后 running/awaiting/canceled 的 exit 帧不可达，不为不可达状态加标题。
- `Renderer.FinalizePendingTools`、`toolStatesOwnCanceledLabel`——其他工具的取消卡语义不变。
- **任何非 exit gate 的确认行落位**（shell、enter_plan_mode、request_permissions、subagent 视图里的确认行）：
  仍然插在被审批的卡之上。
- `pkg/turn` / `pkg/run` 的审批、review 交付、resume 语义；`chat_turn.go` 的 `runAuditStepHook` 与
  `notify.go` 的 `notifyToolStepHooks`（过滤放在 reducer 入口，不在生产者处重复加）。
- **web 前端**（`frontend/src/composables/useChatStream.ts` 的 `upsertApprovalBlock` 也是"插到最后一张工具卡之前"，
  有同源错位）——web 的 transcript 块模型没有 gate 位置锚点，修法需要单独设计，**不在本计划**；见 Maintenance notes。
- `notify.go` / `notify_test.go` 里 owner 未提交的 `markEventShownOnce` 相关改动。
- `pkg/architecture/testdata/graph.json`：本计划不增删 import，不应变化。

## Git workflow

不建分支、不 commit、不 push（owner 手动提交）。改动留在工作区。

## Steps

### Step 1：reducer 入口统一拦截 gate 的等待/取消帧

`pkg/tui/reducer.go` 的 `case MsgKindTool:`（`:1893`）第一行之前插入：

```go
	case MsgKindTool:
		// The exit-plan gate's wait, and a wait abandoned before it resolved,
		// hold no card on any surface (tool.ToolStepHoldsNoCard). Every tool
		// message reaches the transcript through here — the main agent's own
		// step hook, the published-event funnel, replay — so this is the one
		// place the rule is applied. The call still ends the response that
		// preceded it, so the streamed text is sealed exactly as a drawn card
		// would have sealed it.
		if tool.ToolStepHoldsNoCard(firstNonEmpty(msg.ToolName, msg.ToolMeta.ToolName), msg.ToolMeta.Status) {
			return r.flushBufferedText(r.agentBuf(strings.TrimSpace(msg.AgentID)), msg.Timestamp)
		}
		stepID := strings.TrimSpace(msg.StepID)
		...
```

（`firstNonEmpty` 定义在 `pkg/tui/notify.go:1244`，同包可用；`flushBufferedText` 在 `reducer.go:2052`。）

**Verify**：`CGO_ENABLED=1 go build ./...` → exit 0。

### Step 2：删除漏斗处的重复检查

`pkg/tui/notify.go` `publishRunEvent` 内：
- `RunEventToolStarted`：删掉两行注释（"The exit-plan gate's wait paints no card …"）和条件里的
  `&& !tool.ToolStepHoldsNoCard(p.ToolName, "running")`，变成 `if json.Unmarshal(evt.Payload, &p) == nil {`。
- `RunEventToolCompleted`：删掉 `&& !tool.ToolStepHoldsNoCard(p.ToolName, p.ToolMeta.Status)`。

不动该函数其他任何行（尤其是顶部 owner 未提交的 `markEventShownOnce` 段）。若删除后 `notify.go` 不再使用
`tool` 包——不会发生（同文件大量使用 `tool.`），编译即可确认。

**Verify**：`CGO_ENABLED=1 go build ./...` → exit 0；`grep -n 'ToolStepHoldsNoCard' pkg/tui/notify.go` → 无输出；
`grep -rn 'ToolStepHoldsNoCard' pkg/tui --include='*.go' | grep -v _test` → 只剩 `reducer.go`（Step 1）与 `commands.go`（两处既有回放过滤）以及 render.go 注释/新 helper。

### Step 3：无卡 gate 的确认行按产生顺序落位（live）

1. `pkg/tui/render.go`，紧挨 `PrintApprovalConfirmation` 上方新增：

```go
// approvalGateHoldsNoCard reports whether the call an approval gates draws no
// card while it waits — the exit-plan gate (tool.ToolStepHoldsNoCard). The
// records about such a gate, its confirmation line and the plan review it
// asked for, have no parked card to sit above, so they keep the order they
// were produced in: right after the response that issued the call.
func approvalGateHoldsNoCard(actionKind string) bool {
	return tool.ToolStepHoldsNoCard(actionKind, "awaiting approval")
}
```

2. `PrintApprovalConfirmation` 签名改为
   `func (r *Renderer) PrintApprovalConfirmation(agentID, text string, maxLines int, aboveGatedCall bool)`，
   帧里 `InsertBeforeLastTool: aboveGatedCall`。doc 注释补一句：`aboveGatedCall` 为 true 时确认行落在被审批的
   停靠卡之上；无卡 gate 传 false，确认行按产生顺序追加。
3. `pkg/tui/channels.go:1044` 改为：

```go
		// A gate whose call holds no card has no parked block to sit above:
		// its line keeps producer order, right after the response that asked.
		cardless := approvalGateHoldsNoCard(req.ActionKind) || approvalGateHoldsNoCard(approvalToolName(req))
		s.renderer.PrintApprovalConfirmation(req.AgentID, symbol+" "+msg, approvalConfirmationMaxLines, !cardless)
```

   （两个身份都查：确认行措辞用的是 `isApprovalExitPlanMode(req)`——按工具名判断，`overlays.go:1607`；
   回放记录只带 `ActionKind`。live 两者都认，任何一边说是 exit gate 就按无卡处理。）
4. 两个测试调用方 `pkg/tui/chat_session_test.go:17961`、`:18020` 末尾补 `, true`（保持原语义）。

**Verify**：`CGO_ENABLED=1 go build ./... && CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -run 'TestRendererViewportPrintApprovalConfirmation|TestRendererViewportApprovalConfirmationLimitedToThreeLines|TestApprovalSink' -count=1` → ok。

### Step 4：plan review 卡按产生顺序落位，清理死代码

1. `pkg/tui/reducer.go:841-845`：删掉 `InsertBeforeLastTool: fs.Verb == "review",` 及其上方 4 行注释
   （review 只出自 exit gate，而 exit gate 没有停靠卡）。
2. `pkg/tui/reducer.go:4975-4983`（`retainFrameLocked`）：FrameFanout 的 `InsertBeforeLastTool` 分支已不可达，
   整段 if/else 收成一行 `r.vm.replaceOrAppendBlock(f)`。
3. `pkg/tui/tty_session.go:1223-1239`：删除 `replaceOrInsertBeforeLastTool`（含 doc）。
4. `pkg/tui/reducer.go:73-78` `Frame.InsertBeforeLastTool` doc 改为只描述确认行：
   "a tui-only layout hint for an approval confirmation: the line lands above the parked tool block it answers,
   not at the transcript tail. A gate whose call holds no card (approvalGateHoldsNoCard) leaves it false, as do
   ordinary status frames, so they keep producer order."

**Verify**：`CGO_ENABLED=1 go build ./...` → exit 0；
`grep -rn 'replaceOrInsertBeforeLastTool' pkg` → 无输出；
`grep -n 'Verb == "review"' pkg/tui/reducer.go` → 只剩 `:889` 附近那处 `(fs.Verb == "send" || fs.Verb == "review")`（与落位无关，保留）。

### Step 5：回放对无卡 gate 用同一规则

`pkg/tui/commands.go`：

1. 新增一个解析 action kind 的小函数（放在 `approvalEventToolStepID` 旁边，结构照抄它）：

```go
func approvalEventActionKind(evt event.RunEvent) string {
	switch evt.Type {
	case event.RunEventApprovalReq:
		var p event.ApprovalRequestedPayload
		if json.Unmarshal(evt.Payload, &p) != nil {
			return ""
		}
		return strings.TrimSpace(p.ActionKind)
	case event.RunEventApprovalResolved:
		var p event.ApprovalResolvedPayload
		if json.Unmarshal(evt.Payload, &p) != nil {
			return ""
		}
		return strings.TrimSpace(p.ActionKind)
	default:
		return ""
	}
}
```

2. `approvalEventAnchorPosition`：无卡 gate 锚在发出调用的行之后（结果行之前），其余不变：

```go
	stepID := approvalEventToolStepID(evt)
	if stepID == "" {
		return -1
	}
	// A gate whose call holds no card has no card of its own to sit above:
	// its records replay where live printed them, right after the row that
	// issued the call and before whatever answered it.
	if approvalGateHoldsNoCard(approvalEventActionKind(evt)) {
		if row, ok := callRow[stepID]; ok {
			return row + 1
		}
	}
	if row, ok := resultRow[stepID]; ok {
		return row + 1
	}
	...（原逻辑不变）
```

   同时更新该函数 doc：保留原段落，并补一句"A gate that holds no card is the exception: …"说明上面的规则。
3. `replayApprovalEventFrames` 返回帧里 `InsertBeforeLastTool: !approvalGateHoldsNoCard(p.ActionKind),`。

**Verify**：`CGO_ENABLED=1 go build ./... && CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -run 'TestReplayTimelinePutsThePlanReviewCardBetweenTheApprovalLines|TestReplayDrawsNoCardForCanceledExitGate|TestReplayDropsCanceledExitGateSettledRow' -count=1` → ok
（前者无需修改即应通过：它的事件改锚到 call 行之后、结果卡之前，`asked < card < approved < tool` 仍成立。若不通过 → STOP 2）。

### Step 6：测试（新增 + 改写）

见下方 "Test plan"，逐条实现。

**Verify**：`CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -count=1` → ok。

### Step 7：全量回归

```bash
CGO_ENABLED=1 go test -tags fts5 ./pkg/... ./cmd/forebrain -count=1
go vet ./...
gofmt -l pkg cmd
git diff --stat 9656051 -- pkg/architecture/testdata/graph.json
```

**Verify**：测试 ok（gateway TempDir flake 按上表处理）；vet、gofmt 无输出；graph.json 无 diff。

### Step 8：真机验收（driver，独立 WORK/SESSION/PORT）

```bash
export FOREBRAIN_RUN_WORK=$TMPDIR/fb-007 FOREBRAIN_RUN_SESSION=fb-007 FOREBRAIN_RUN_PORT=8807
D=.claude/skills/run-forebrain/driver.sh
$D build
```

每个场景前 `$D reset`。toolcall 模式下 enter_plan_mode 也要审批，先 `$D key Enter` 批准。

- **A 停靠无卡**：`$D start toolcall; $D submit 'plan something'; $D wait 'Yes, proceed' 25; $D key Enter; $D wait 'Ask another model' 25; sleep 1; $D screen`
  → `$D screen | grep -c 'Exited plan mode'` = **0**；`● calling exit_plan_mode` 下面紧接审批 overlay。
- **B Ctrl+C 取消**：接 A，`$D key C-c; sleep 2; $D screen`
  → `$D screen | grep -c '◆ Canceled'` = **0**；`$D screen | grep -c 'Exited plan mode'` = **0**；
  `$D screen | grep -n 'Entered plan mode\|calling exit_plan_mode\|You canceled'` 行号严格递增（确认行在 `● calling exit_plan_mode` 之后）。
- **C 回放 B**：`SID=$($D sid); $D stop; RESUME_ID=$SID $D start reply; sleep 3; $D screen`
  → 与 B 相同顺序，无 "Exited plan mode"、无 "◆ Canceled"。
- **D Esc 取消**：重复 A，然后 `$D key Escape` → 同 B 的断言。
- **E review（driver 下 review 因无计划文件失败，但足以验证落位）**：重复 A，`$D key Down Down Down; $D key Enter; sleep 1; $D key Enter; sleep 4; $D screen`
  → 顺序为 `● calling exit_plan_mode` → `✔ You asked … to review the plan` → `● Failed to start 1 plan-reviewer task`，
  **无** "Exited plan mode"；overlay 重新提示。随后 `$D key Down; $D key Enter`（选 "2. Yes, proceed"）
  → 末尾为 `✔ You approved forebrain to exit plan mode` → `◆ Exited plan mode`。
  回放该会话（同 C 的方法）顺序一致。
- **F 拒绝带反馈**：重复 A，`$D key Down Down; $D key Enter; $D send '先拆分里程碑'; $D key Enter; sleep 2`
  → `✗ You did not approve …` 之后是 `◆ Kept planning └ 先拆分里程碑`；回放一致。

验收后 `$D stop`，`rm -rf $TMPDIR/fb-007`。
**真实 provider 的成功 review 路径**（review 交付 → 模型修订 → 新 gate）driver 无法驱动：在计划验收记录里写明，交 owner 用真实
模型复核一次：review 期间无 "Exited plan mode" 卡；交付后修订内容与新 gate 依次出现在 review 卡之后。

### Step 9：文档勘误

在 `docs/plan/EXIT_PLAN_GATE_GHOST_CARD_PLAN.md` 与 `docs/plan/PLAN_REVIEW_DELIVERY_SILENT_PLAN.md` 文件头的第一个
引用块之后各加一段：

```markdown
> **勘误（2026-10-10，见 `plans/007-exit-gate-live-card-and-cardless-anchoring.md`）**：主 agent 的工具步骤经
> `runAuditStepHook → notifyToolStepHooks` 直接进 UI，不经 `publishRunEvent` 漏斗，所以本文"TUI live 已干净 /
> 等待卡已删除"的结论不成立；gate 等待帧仍被画成 "Exited plan mode"、取消后被改成 "Canceled"。007 把过滤移到
> reducer 入口，并让无卡 gate 的确认行与 review 卡按产生顺序落位（live 与回放一致）。
```

**Verify**：`grep -c '勘误（2026-10-10' docs/plan/EXIT_PLAN_GATE_GHOST_CARD_PLAN.md docs/plan/PLAN_REVIEW_DELIVERY_SILENT_PLAN.md` → 各 1。

## Test plan

所有新测试放在下列文件，结构照抄所列样板。

1. **reducer 入口**（`pkg/tui/reducer_test.go`，新增 `TestReducerHoldsNoCardForTheExitGateWait`；样板：同文件
   `var r Reducer; r.Reduce(...)` 的用法，如 `:2271-2273`）：
   - 表驱动：`exit_plan_mode` + status `running` / `awaiting approval` / `canceled` → `Reduce(NewMessageMsg{Msg: Message{Kind: MsgKindTool, StepID: "call-exit", ToolName: "exit_plan_mode", ToolMeta: tool.ToolMeta{ToolName: "exit_plan_mode", Status: s}}})` 的 `Frames` 里**没有** `FrameTool`；
   - `completed` / `denied` / `failed` → 恰好一个 `FrameTool`；
   - 对照：`shell` + `running` → 一个 `FrameTool`；
   - 封存：先 `Reduce(NewMessageMsg{Msg: Message{Kind: MsgKindAssistant, Content: "calling exit_plan_mode"}})`，再送 `running` 的 gate 消息 → 返回帧恰为一个 `Final == true` 的 `FrameAssistant`，内容 "calling exit_plan_mode"。
2. **真正漏网的生产者**（`pkg/tui/chat_session_test.go`，新增 `TestMainAgentExitGateStepsDrawNoCard`；样板：
   `TestNotifyToolStepHooksUsesLocalNotificationHookWhenNoExternalHook`，`:23877-23912`，`newSurfaceTestSession` +
   `PrependUINotify` 收集 `NewMessageMsg`）：调 `s.notifyToolStepHooks` 两次——`tool.StepEvent{Kind: tool.StepKindToolStarted, StepID: "call-exit", ToolName: "exit_plan_mode"}` 与
   `tool.StepEvent{Kind: tool.StepKindToolCompleted, StepID: "call-exit", ToolName: "exit_plan_mode", ActionID: "act-1", ActionKind: "exit_plan_mode", Output: map[string]any{"requires_action": true}}`；
   用 `require.Eventually` 收齐两条消息后，逐条喂给一个 `Reducer`，断言没有 `FrameTool`。
3. **取消不再出 "Canceled"**（`pkg/tui/reducer_test.go`，新增 `TestCanceledExitGateLeavesOnlyItsConfirmationLine`；
   样板：`newComposerRenderer(t, 100, 40)` + `r.RenderFrame` + 遍历 `r.vm.blocks`，见 `:2293-2330`）：
   渲染一张 settled 的 `enter_plan_mode` 工具卡（`Frame{Kind: FrameTool, StepID: "call-enter", Title: "enter_plan_mode", ToolMeta: tool.ToolMeta{ToolName: "enter_plan_mode", Status: "completed"}, Final: true}`）；
   经 reducer 送 assistant 文本 + gate 的 running、awaiting approval 两条消息并渲染返回帧；
   `r.PrintApprovalConfirmation("", "✗ You canceled forebrain's request to exit plan mode", approvalConfirmationMaxLines, false)`；
   `reducer.Reduce(StreamResetMsg{})`；`r.FinalizePendingTools()`。断言 blocks 顺序为
   `[enter 卡, assistant "calling exit_plan_mode", status "You canceled…"]`，且没有任何 `ToolMeta.Status == "canceled"` 的块。
4. **确认行落位**（`pkg/tui/chat_session_test.go`，新增 `TestApprovalSink_CardlessGateConfirmationKeepsProducerOrder`；样板：
   `TestApprovalSink_SubagentConfirmationStaysInTheSubagentView`，`:22174-22210`）：先渲染一张 settled 工具卡
   （shell，`call-1`）与一条 final assistant 帧；
   `req := turn.ToolApprovalRequest{ActionID: "act-exit", ActionKind: "exit_plan_mode", ToolName: "exit_plan_mode"}`，
   `decision := turn.ToolApprovalDecision{Cancelled: true}`，经 `NewInteractiveApprovalSinkWithTTY(&out, nil).WithRenderer(r).printApprovalConfirmation(req, decision)`
   → 确认行是最后一个块。对照子测试：`ActionKind: "shell"` 的请求仍插在最后一张工具卡之前（与既有测试 `:17954-17975` 语义一致）。
   若 `approvalConfirmationLine` 对该 req 返回空行，补齐 req 字段直到返回 "You canceled forebrain's request to exit plan mode"（参照 `channels.go:1110-1135` 的判定）。
5. **改写** `TestPlanReviewCardLandsBetweenTheApprovalConfirmations`（`pkg/tui/reducer_test.go:2288-2340`）
   → `TestPlanReviewKeepsProducerOrderAroundTheCardlessGate`：去掉"先渲染停靠的 exit_plan_mode 卡"那一行；
   在 assistant 帧之前先渲染一张 settled 的 `write_file` 工具卡（代表写计划文件）；确认行帧 `InsertBeforeLastTool: false`；
   review 卡由 `reducer.Reduce(PlanReviewStartedMsg{…})` 产生；最后渲染批准后的 completed exit 卡
   （`Frame{Kind: FrameTool, StepID: "call-exit", Title: "exit_plan_mode", ToolMeta: tool.ToolMeta{ToolName: "exit_plan_mode", Status: "completed"}, Final: true}`）。
   期望顺序 `["write_file 卡", "assistant", "status:asked", "card:rv-1", "status:approved", "exit plan mode"]`；
   保留原测试后半段"同 StepID 的 review 更新原地替换、不新增块"的断言。doc 注释按新语义重写（无停靠卡、按产生顺序）。
6. **回放：取消**（`pkg/tui/commands_test.go`，新增 `TestReplayPlacesCardlessGateConfirmationAfterItsCall`；样板：
   `TestReplayDrawsNoCardForCanceledExitGate`（`:2139-2186`）的 renderer 构造 + `replayTimelineWithReducer`，
   之后调 `flushReplayReducer(renderer, reducer)`，再遍历 `renderer.vm.blocks` 按 `Kind` 定位——工具卡用
   `Kind == FrameTool && Title == "enter_plan_mode"`，assistant 用 `Kind == FrameAssistant && strings.Contains(Content, "calling exit_plan_mode")`，
   确认行用 `Kind == FrameStatus && strings.Contains(Content, "You canceled")`。不要用纯子串匹配
   `replayedTimelineOrder` 的输出：assistant 文本 "calling enter_plan_mode" 也含工具名，会误中。行构造助手：
   `assistantToolCallRow`、`toolRowWithDisplay`（`:2120-2200`））：
   turns = user → assistant（`enter_plan_mode` 调用 `call-enter`）→ enter 的 tool 行（`{"tool_name":"enter_plan_mode","status":"completed"}`）→
   `assistantToolCallRow("calling exit_plan_mode", "call-exit", "exit_plan_mode", "{}")` → exit 的 canceled 行
   （`{"tool_name":"exit_plan_mode","status":"canceled"}`）；events = enter 的 approved 确认 + exit 的 cancelled 确认（带 `ActionKind`、`ToolStepID`、`Confirmation`，构造方式照 `TestReplayDrawsNoCardForCanceledExitGate`）。
   断言：含 "You canceled" 的块下标 **大于** 含 "calling exit_plan_mode" 的块下标，后者大于 enter 卡的下标；没有 exit 工具卡。
   （行的 `RowID` 依次设为 1..5。）
7. **回放：批准/拒绝**（同文件，表驱动加在 6 旁边，`TestReplayCardlessGateDecisionPrecedesItsSettledCard`）：
   exit 行改为 `completed` / `denied`（拒绝时 body 为反馈文字），确认行分别为 "✔ You approved forebrain to exit plan mode" /
   "✗ You did not approve forebrain to exit plan mode"。断言顺序：assistant "calling exit_plan_mode" < 确认行 < settled 卡（"Exited plan mode" / "Kept planning"）。
8. **回放：review 交付后继续**（同文件，`TestReplayDeliveredReviewKeepsTheRevisedTurnBelowIt`）：
   turns = user → `assistantToolCallRow("here is the plan", "call-exit-a", "exit_plan_mode", "{}")` →
   `toolRowWithDisplay(3, "call-exit-a", tool.PlanReviewDeliveredDisplayKey, `{"tool_name":"exit_plan_mode","status":"denied"}`)` →
   `assistantToolCallRow("revised plan", "call-exit-b", "exit_plan_mode", "{}")` →
   `toolRowWithDisplay(5, "call-exit-b", `{"mode":"agent","message":"Exited plan mode."}`, `{"tool_name":"exit_plan_mode","status":"completed"}`)`
   （body 不能为空：`replayToolMessage` 对没有可显示输出的行返回 ok=false，整行不画）；
   events（按 sequence 1..N）：A 的 review_requested 确认（"✔ You asked zhipuai/glm-5.3-flash to review the plan"）、
   `plan_review_started`/`plan_reviewed`（结构照 `TestReplayTimelinePutsThePlanReviewCardBetweenTheApprovalLines`）、
   A 的 denied 交付记录（Confirmation 为 `tool.PlanReviewDeliveredDisplayKey`，应被丢弃）、B 的 approved 确认。
   断言顺序：`here is the plan` < `You asked` < review 卡 < `revised plan` < `You approved` < "Exited plan mode" 卡；
   全程只有一张 exit 工具卡（B 的）。这是 owner 截图 1 的真实流程。
9. 既有 `TestReplayTimelinePutsThePlanReviewCardBetweenTheApprovalLines`、`TestReplayDrawsNoCardForCanceledExitGate`、
   `TestReplayDropsCanceledExitGateSettledRow` **不改**、必须仍通过。

**Verify**：`CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -count=1` → ok，含上述 7 个新测试与 1 个改写测试。

## Done criteria

- [ ] `CGO_ENABLED=1 go test -tags fts5 ./pkg/... ./cmd/forebrain -count=1` 全绿（gateway TempDir flake 已按说明复跑确认）
- [ ] `go vet ./...`、`gofmt -l pkg cmd` 无输出
- [ ] `go test -tags fts5 ./pkg/tui -run 'TestReducerHoldsNoCardForTheExitGateWait|TestMainAgentExitGateStepsDrawNoCard|TestCanceledExitGateLeavesOnlyItsConfirmationLine|TestApprovalSink_CardlessGateConfirmationKeepsProducerOrder|TestPlanReviewKeepsProducerOrderAroundTheCardlessGate|TestReplayPlacesCardlessGateConfirmationAfterItsCall|TestReplayCardlessGateDecisionPrecedesItsSettledCard|TestReplayDeliveredReviewKeepsTheRevisedTurnBelowIt' -count=1 -v` 显示 8 个 PASS
- [ ] `grep -rn 'replaceOrInsertBeforeLastTool' pkg` 无输出；`grep -n 'ToolStepHoldsNoCard' pkg/tui/notify.go` 无输出
- [ ] `git diff --stat 9656051 -- pkg/architecture/testdata/graph.json` 无输出
- [ ] Step 8 场景 A–F 的 capture 断言全部满足，截图/输出摘要写进本计划末尾"验收记录"
- [ ] `git status` 中本计划的改动只涉及 Scope 列出的文件（owner 既有的未提交改动除外）
- [ ] `plans/README.md` 中 007 状态更新

## STOP conditions

1. Drift check 发现 in-scope 文件（`notify.go` 已知的 owner 改动除外）与摘录不一致。
2. Step 5 后 `TestReplayTimelinePutsThePlanReviewCardBetweenTheApprovalLines` 失败——说明回放锚点与 review 事件的
   sequence 交互和计划推演不同，停下报告实际顺序。
3. 任何**非 exit gate** 的确认行落位测试变红（shell / request_permissions / subagent 视图），或修改需要让这些路径改变行为。
4. 新测试 2 证明 `notifyToolStepHooks` 产出的消息里 `ToolMeta.Status` 不是 `running` / `awaiting approval`
   （那么 reducer 入口过滤命中不了），停下报告实际状态值，不要在生产者处另加过滤。
5. 真机场景 B/D 中确认行仍出现在 `◆ Entered plan mode` 之上，或 `● calling exit_plan_mode` 这行在取消后消失
   （封存逻辑没生效）。
6. 需要增删 import（架构图会变）或改动 Out of scope 中的任何文件。

## Maintenance notes

- **规则的唯一入口**：TUI 里 exit gate 不画卡的规则现在只在 `Reducer.reduceMessage` 的 `case MsgKindTool` 应用一次；
  回放侧 `commands.go` 的两处既有过滤（`replayToolMessage` 整行丢弃、`orphanToolCalls` 不重建）仍保留，它们还承担
  "不参与 plan 卡接管 / 孤儿落位"的职责。新增任何工具消息生产者都无需再加过滤——这正是上一轮漏掉主 agent 路径的教训。
- **"无卡 gate"有两层含义，改一处要同步另一处**：`tool.ToolStepHoldsNoCard`（哪些帧不画）与
  `approvalGateHoldsNoCard`（哪些审批的记录没有停靠卡可依附）。将来若别的 gate 也改成无卡，两者一起生效；若 exit gate
  重新画等待卡，确认行与 review 卡必须恢复"插到停靠卡之上"。
- `render.go` exit_plan_mode 标题表的 else 分支只服务 completed；不要为 running/awaiting/canceled 加标题（不可达）。
- 回放只凭记录里的 `action_kind` 认出无卡 gate。没有 `action_kind` 的旧记录退回旧规则（锚在结果行后、插到最后一张
  工具卡之前），表现与修复前一致，不会更差；若要覆盖，可改为用 `callIndex[stepID].name` 兜底。
- 回放把无卡 gate 的记录锚在"发出调用的 assistant 行之后"。若同一 assistant 行并行发出多个调用且 exit gate 不是最后一个，
  确认行会出现在所有兄弟结果卡之前（live 则在已完成的兄弟卡之后）。exit_plan_mode 实际不会与其他调用并行，接受此差异；
  若将来出现，按调用顺序细化锚点。
- **web 未同步（需 owner 决定是否另立计划）**：`frontend/src/composables/useChatStream.ts` 的 `upsertApprovalBlock`
  对无卡 gate 走"插到最后一张工具卡之前"的回退（注释明说是为了和 TUI 一致），测试
  `useChatStream.test.ts:2325-2331`、`:2904-2906` 把这个错位钉成了期望。TUI 修好后两端不再一致。web 的难点是 transcript
  块里没有 gate 调用的位置（被丢弃的 canceled/交付行不进 `gateStepIds`，assistant 块也不记它发出的调用），
  需要一个锚点模型（例如隐藏的 gate 锚块，或在 `gateStepIds` 里记位置）——属于设计取舍，不在本计划。
- 评审时重点看：Step 1 的封存是否在所有 agentID 下都取对缓冲区；Step 5 对 `planReviewAnchorByAction` 的连带效果
  （它复用 `approvalEventAnchorPosition`）。
