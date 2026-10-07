# PLAN_REVIEW_CARD_ORDERING — 让 plan-reviewer 卡片落在「asked … approved」之间

Planned at: 工作区基线 `git rev-parse --short HEAD` = `88eb320`，且带 owner 未提交改动
（`pkg/tui/{render.go,render_test.go,chat_session.go,chat_session_test.go,commands.go,commands_test.go,run.go,run_test.go,notify.go,chat_slash.go,setup.go}`、
`pkg/gateway/*`、`README.md` 等）。实施只做手术式 `edit_file` 精确匹配，
**绝不整文件覆盖 / cp 快照恢复**——owner 与执行者并行改同一工作区。
Drift 检查以本计划的 file:line 对照当前源码（行号可能已位移，锚点以代码内容为准）。

批准后第一步：把本计划全文落盘到仓库 `docs/plan/PLAN_REVIEW_CARD_ORDERING_PLAN.md`，
然后按步骤实施；全部改动留在工作区，**不 commit**。

---

## 1. 目标（Why this matters）

用户在 plan 模式下选择「用其他模型评审计划」时：plan-reviewer 子代理**先**评审，
用户**后**批准 `exit_plan_mode`。因此 TUI 转录里三者必须按
`✔ You asked … to review the plan` → `plan-reviewer 卡片` → `✔ You approved forebrain to exit plan mode`
的顺序出现。现在卡片被排到了最后（`Exited plan mode` 工具块之后、两条确认行之下），
把「先评审、后批准」的因果读反了。要求：**live 与 resume replay 都必须把卡片夹在两条确认行之间**
（产品常设要求：replay 与 live 1:1）。

## 2. 根因（file:line 证据链 + 现场证据）

### 2.1 现场证据（真实 session `cli-045f699d-3e8f-43e6-91f6-5cc50915fce7`，`~/.forebrain/state/forebrain.state.sqlite`）

`fb_session_events` 按 `sequence`：

```
3704238 tool_call_started   step=call_362e159e75d64f85af1eb98f   ← exit_plan_mode 的工具卡 T 诞生
3704239 tool_call_completed requires_action (parked)              ← 调用挂起等审批
3704240 approval_requested  action_id=d39b395b-…-e5cc1c9f25ac
3704241 approval_resolved   decision=review_requested             "✔ You asked deepseek / deepseek-v4-flash to review the plan"
3704242 plan_review_started review_id=plan-review:141502c5-…
3722718 plan_reviewed       duration_ms=152353                    （= 2m32s，截图卡片时长）
3722720 approval_resolved   decision=approved                      "✔ You approved forebrain to exit plan mode"
3722722 tool_call_started   step=call_362e159e…（批准后重放）
3722723 tool_call_completed Exited plan mode
```

评审确实发生在「asked」之后、「approved」之前。reviewer 的 `subagent_spawned` payload：
`parent_run_id=651ffad2…`（gated run）、`parent_tool_call_id=plan-review:141502c5…`（不是真实工具行）。

### 2.2 根因

布局决定点在 `Renderer.retainFrameLocked`（`pkg/tui/reducer.go:4907`）：

- **批准确认行**是 `FrameStatus{InsertBeforeLastTool: true}`，走
  `insertBeforeLast(prev.Kind == FrameTool)`（`pkg/tui/reducer.go:4972-4977`），
  落到「最后一个工具块」T（exit_plan_mode）**上方**。
- **评审卡片**是 `FrameFanout`，走 `replaceOrAppendBlock`（`pkg/tui/reducer.go:4967-4968`），
  首次**追加到 transcript 尾部**（`pkg/tui/tty_session.go:1207-1221`）。

T 在评审开始前就已进入 transcript（现场证据 3704238 < 3704242），且评审期间没有别的
`FrameTool` 落到主转录（reviewer 自己的工具调用带 `AgentID`，路由进它的 per-agent 视图，
见 `pkg/tui/reducer.go:1936-1942`）。所以：

```
块序列（live）
1. A = assistant「计划已写好，请求审批。」
2. exit_plan_mode 工具卡 T      → [A, T]
3. asked  InsertBeforeLastTool  → [A, asked, T]
4. PlanReviewStarted  append    → [A, asked, T, C]        ← 卡片被推到 T 之后
5. approved InsertBeforeLastTool→ [A, asked, approved, T, C]
6. T 完成，同 StepID 原位替换    → [A, asked, approved, T, C]   == 截图
期望:                            [A, asked, C, approved, T]
```

### 2.3 replay 侧同样错位（实测同一 session）

`replayTimelineWithReducer`（`pkg/tui/commands.go:261`）给每条事件算落点 `pos`：
`approvalEventAnchorPosition`（`pkg/tui/commands.go:517-530`）只认 `approval_*` 事件（按
`tool_step_id` 命中工具行）；`plan_review_started/reviewed` 不含 `tool_step_id` → `pos = -1` →
`anchors[evt.RunID]` 也没有这条（reviewer 的 `parent_tool_call_id` 不是真实工具行）→
回退 `transcriptRowsBefore(clock)`（`pkg/tui/commands.go:334`）。
实测该 session：`plan_review_started` 的落点算到了 plan 轮次起始行（早于 assistant
「计划已写好」），而批准记录锚在 `resultRow[call_362e…]+1`。→ replay 也会把卡片排到
确认行之前/之外。**live 与 replay 必须同时修**。

## 3. 修复设计

把评审卡片归一到「批准确认行」已有的布局规则：**它就是审批交换的一部分，
属于「最后一个工具块之前」**。

1. **live（`retainFrameLocked`）**：给 plan-review 卡片一个「首次插入到最后一个工具块之前、
   之后按 StepID 原位更新」的落点；用现有 `Frame.InsertBeforeLastTool` 作为标记（只有
   plan-review 卡片会置位）。
2. **replay（`replayTimelineWithReducer`）**：把 `plan_review_started/reviewed` 锚定到它
   所属 approval action 的工具行——与批准记录同一个 `pos` 组；组内再按 `sequence` 排序，
   天然得到 `asked(3704241) → started(3704242) → reviewed(3722718) → approved(3722720)`。

### 行为变化矩阵

| 场景 | 旧行为 | 新行为 | 备注 |
|---|---|---|---|
| live：请求评审后卡片出现位置 | 追加到尾部（exit_plan_mode 工具块之后） | 插入到最后工具块之前（asked 之后、approved 之前） | 本修复目标 |
| live：卡片后续更新（spawn/steps/reviewed） | 同 StepID 原位替换 | 不变（同 StepID 原位替换） | |
| replay：卡片落点 | clock 回退（落到 plan 轮次起点/更早） | 锚到 approval action 的工具行，与确认行同组 | 本修复目标 |
| 其他 fanout 卡片（subagent_run/fanout/status/…） | 尾部追加 | **不变**（`Verb != "review"` 不置位） | 回归保护 |
| reviewer 自身工具调用 | 进 per-agent 视图 | **不变** | |
| web / gateway | 评审渲染在 approval 卡片体内（`plan_review_models`/`plan_reviews` wire），非 transcript 卡片 | **不变** | 本 bug 是 TUI 转录投影专属 |

## 4. Scope

**In scope**（只改这些文件）：
- `pkg/tui/tty_session.go`（新增一个 `viewModel` 落点方法）
- `pkg/tui/reducer.go`（`emitFanoutFrame` 置标记 + `retainFrameLocked` 路由 + 字段注释）
- `pkg/tui/commands.go`（replay 锚定）
- `pkg/tui/reducer_test.go`、`pkg/tui/commands_test.go`（新增回归测试）

**Out of scope**（看似相关，明确不碰）：
- `pkg/turn/**`、`pkg/event/**`：事件顺序与 payload 已正确，不改事件模型。
- `pkg/gateway/**`、`frontend/**`：web 的评审在 approval 卡片内呈现，无 transcript 卡片，天然无此顺序问题。
- `pkg/tui/channels.go` 的 `approvalConfirmationText`/`printApprovalConfirmation`：确认行机制已正确，复用其规则即可。
- `pkg/tui/render.go` 的 fanout 渲染：卡内容渲染无关。
- 任何与 `/diff`、`/help` 删除相关的 owner 未提交改动：不做顺手改动。

## 5. Commands you will need

| Purpose | Command | Expected on success |
|---|---|---|
| Build | `CGO_ENABLED=1 go build ./...` | exit 0 |
| 定点测试 | `CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -run 'PlanReview\|ReplayTimeline' -count=1` | all pass |
| 包级回归 | `CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -count=1` | all pass |
| vet（CI 同款） | `go vet ./...` | exit 0 |
| 真机构建 | `go build -tags fts5 -o ./build/bin/forebrain ./cmd/forebrain` | exit 0 |

## 6. Steps

### Step 1 — `viewModel` 增加「替换或插入到最后一个工具块之前」落点

文件：`pkg/tui/tty_session.go`。紧挨现有 `replaceOrAppendBlock`（当前 :1207-1221）与
`insertBeforeLast`（当前 :1174-1190）新增：

```go
// replaceOrInsertBeforeLastTool is replaceOrAppendBlock for a frame that
// belongs to an approval exchange: a block already carrying this StepID is
// updated in place, and a new one lands immediately before the last tool
// block — beside the approval's own confirmation lines — instead of at the
// tail, where the parked call the approval is holding already sits.
func (m *viewModel) replaceOrInsertBeforeLastTool(f Frame) *viewBlock {
	if f.StepID != "" {
		for _, b := range m.blocks {
			if b.frame.Kind == f.Kind && b.frame.StepID == f.StepID && !b.frame.RetainAsHistory {
				b.frame = f
				b.cache.valid = false
				return b
			}
		}
	}
	return m.insertBeforeLast(func(prev Frame) bool { return prev.Kind == FrameTool }, f)
}
```

**Verify**：`CGO_ENABLED=1 go build ./pkg/tui` → exit 0。

### Step 2 — 给 plan-review 卡片置位 `InsertBeforeLastTool`

文件：`pkg/tui/reducer.go`。

(a) `emitFanoutFrame`（当前 :824-849）返回的 `Frame{...}` 里加一行
`InsertBeforeLastTool: fs.Verb == "review",`（`fs.Verb` 取值见 `fanoutState` 文档
:265-289：`"run"/"send"/"continue"/"status"/"wait"/"close"/"list"/"review"`，
只有 plan-review 卡片用 `"review"`，由 `openPlanReviewCard` :1157 设置）。

(b) 更新 `Frame.InsertBeforeLastTool` 字段注释（当前 :73-76），把「approval confirmation
statuses」改为「approval-exchange frames（确认行 + plan-review 卡片）」，并保留
「ordinary status frames must leave it false」。

**Verify**：`grep -n 'InsertBeforeLastTool' pkg/tui/reducer.go` → 出现在字段注释、
`emitFanoutFrame`、以及两处状态分支。

### Step 3 — `retainFrameLocked` 按标记路由

文件：`pkg/tui/reducer.go`，当前 :4967-4971：

```go
	if f.Kind == FrameFanout || f.Kind == FrameTool || f.Kind == FrameMemoryCompact || f.Kind == FrameSkillInstall {
		r.vm.replaceOrAppendBlock(f)
		if f.Kind == FrameMemoryCompact || f.Kind == FrameFanout {
			r.syncLiveBlockAnimationLocked()
		}
	}
```

改为：

```go
	if f.Kind == FrameFanout || f.Kind == FrameTool || f.Kind == FrameMemoryCompact || f.Kind == FrameSkillInstall {
		if f.Kind == FrameFanout && f.InsertBeforeLastTool {
			r.vm.replaceOrInsertBeforeLastTool(f)
		} else {
			r.vm.replaceOrAppendBlock(f)
		}
		if f.Kind == FrameMemoryCompact || f.Kind == FrameFanout {
			r.syncLiveBlockAnimationLocked()
		}
	}
```

说明：上面的 per-agent 分支（:4950-4963）不处理——plan-review 卡片的 `OwnerAgentID`
为空（`openPlanReviewCard` 不设），永远不会进 per-agent 视图。

**Verify**：`CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -run 'PlanReview' -count=1` → pass。

### Step 4 — replay 锚定 plan-review 事件到其 approval action 的工具行

文件：`pkg/tui/commands.go`。

(a) 新增两个 helper（放在 `approvalEventAnchorPosition` 附近，当前 :517 起）：

```go
// planReviewAnchorByAction maps each approval action to the transcript position
// its records replay at: the tool call the action names. A plan review's card
// then shares one position with the confirmation lines of the approval it
// answers, and the group's own sequence order puts the card between them.
func planReviewAnchorByAction(events []event.RunEvent, callRow, resultRow map[string]int) map[string]int {
	out := make(map[string]int)
	for i := range events {
		switch events[i].Type {
		case event.RunEventApprovalReq, event.RunEventApprovalResolved:
		default:
			continue
		}
		var p struct {
			ActionID string `json:"action_id"`
		}
		if json.Unmarshal(events[i].Payload, &p) != nil || strings.TrimSpace(p.ActionID) == "" {
			continue
		}
		id := strings.TrimSpace(p.ActionID)
		if _, seen := out[id]; seen {
			continue
		}
		if pos := approvalEventAnchorPosition(events[i], callRow, resultRow); pos >= 0 {
			out[id] = pos
		}
	}
	return out
}

// planReviewEventActionID is the approval action a plan-review event answers.
func planReviewEventActionID(evt event.RunEvent) (string, bool) {
	switch evt.Type {
	case event.RunEventPlanReviewStarted:
		var p event.PlanReviewStartedPayload
		if json.Unmarshal(evt.Payload, &p) != nil {
			return "", false
		}
		return strings.TrimSpace(p.ActionID), true
	case event.RunEventPlanReviewed:
		var p event.PlanReviewedPayload
		if json.Unmarshal(evt.Payload, &p) != nil {
			return "", false
		}
		return strings.TrimSpace(p.ActionID), true
	default:
		return "", false
	}
}
```

(b) 在 `replayTimelineWithReducer` 里，`pending := make(...)`（当前 :272）之后加：

```go
	reviewAnchors := planReviewAnchorByAction(events, callRow, resultRow)
```

(c) 在落点计算处（当前 :316-327），`approvalEventAnchorPosition` 之后、`anchors` 回退之前插入：

```go
		if pos < 0 {
			if id, ok := planReviewEventActionID(evt); ok {
				if reviewPos, found := reviewAnchors[id]; found {
					pos = reviewPos
				}
			}
		}
```

**Verify**：`CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -run 'Replay' -count=1` → pass。

### Step 5 — 回归测试

**(a) live 顺序测试**（`pkg/tui/reducer_test.go`，新 `TestPlanReviewCardLandsBetweenTheApprovalConfirmations`）：
构造 `NewRenderer(nil,nil)` + `EnableViewportMode()`；依次
`RenderFrame(Frame{Kind: FrameTool, StepID: "call-exit", Title: "exit plan mode"})`（T）；
`RenderFrame(Frame{Kind: FrameStatus, Content: "✔ You asked m to review the plan", InsertBeforeLastTool: true})`；
`for f := range (&Reducer{}).Reduce(PlanReviewStartedMsg{ReviewID:"rv-1", Provider:"p", Model:"m"}).Frames { r.RenderFrame(f) }`；
`RenderFrame(Frame{Kind: FrameStatus, Content: "✔ You approved forebrain to exit plan mode", InsertBeforeLastTool: true})`。
断言 `r.vm.blocks` 顺序为：asked、card(FrameFanout stepID=rv-1)、approved、tool。
再断言二次 `Reduce(PlanReviewStartedMsg{...})` 不新增块（同 StepID 原位替换）。

**(b) replay 顺序测试**（`pkg/tui/commands_test.go`，新 `TestReplayTimelinePutsThePlanReviewCardBetweenTheApprovalLines`）：
照 `TestReplayTimelineAnchorsSubagentCardsToTheCallThatSpawnedThem`（当前 :1748）的结构，
用 helper `replayedTimelineOrder(t, turns, events)`（当前 :1697）：
- `turns`：user 行；assistant 行（发起 `exit_plan_mode` 调用 `call-exit`）；tool 行
  `ToolStepID:"call-exit"`；再一条 assistant 行。
- `events`（`Sequence` 递增）：`approval_requested(act-1, ToolStepID:"call-exit")`、
  `approval_resolved(act-1, decision review_requested, Confirmation "✔ You asked …")`、
  `plan_review_started(action_id:"act-1", review_id:"plan-review:rv-1")`、
  `plan_reviewed(action_id:"act-1", review_id:"plan-review:rv-1")`、
  `approval_resolved(act-1, decision approved, Confirmation "✔ You approved …")`。
- 断言顺序含：`✔ You asked …` → `Ran 1 plan-reviewer task` → `✔ You approved …` →
  exit_plan_mode 工具卡。

**Verify**：`CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -run 'PlanReview|ReplayTimeline' -count=1` → pass。

### Step 6 — 全量验证 + tmux 真机验收

```
CGO_ENABLED=1 go build ./...
CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -count=1
go vet ./...
go build -tags fts5 -o ./build/bin/forebrain ./cmd/forebrain
```

tmux 真机（临时 `FOREBRAIN_HOME` + 临时项目目录，**不污染真实 `~/.forebrain`**；
构建必须带 `-tags fts5`，否则 TUI 报 `no such module: fts5` 退出，那是工具链问题不是产品 bug）：

```
tmux new-session -d -s prc -x 120 -y 42 \
  'cd <临时项目> && FOREBRAIN_HOME=<临时home> ./build/bin/forebrain; echo EXIT_CODE=$?; sleep 300'
```
驱动：进入 plan 模式 → 让 agent 写好计划请求审批 → 在审批浮层选择「用其他模型评审计划」
（配置一个可用 provider）→ 用 `tmux capture-pane -t prc -p` 逐屏取证，断言
`You asked …` 在 `Ran 1 plan-reviewer task` 之上、`You approved forebrain to exit plan mode`
在卡片之下；否则重跑。收尾 `tmux kill-session -t prc` + 删临时目录。

## 7. Done criteria（机器可查）

- [x] `CGO_ENABLED=1 go build ./...` exit 0；`go vet ./...` exit 0。
- [x] `CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -count=1` 全绿（41.2s），含两个新测试。
- [x] `grep -n 'InsertBeforeLastTool: fs.Verb == "review"' pkg/tui/reducer.go` 有输出（:842）。
- [x] `grep -n 'planReviewAnchorByAction' pkg/tui/commands.go` 有输出（定义 + `replayTimelineWithReducer` 调用点）。
- [x] `git status --porcelain` 改动仅落在 Scope 列出的文件 + `docs/plan/PLAN_REVIEW_CARD_ORDERING_PLAN.md`（`reducer.go`/`reducer_test.go`/`tty_session.go` 本次会话前是干净的，diff 只有本修复）。
- [x] tmux 真机取证：live 与 resume replay 双路径均为 `asked → card → approved → 工具块`（见 Implementation Status）。

## 8. STOP conditions

- 「Current state」里的代码摘录与实际不符（owner 并行改动导致漂移）→ 停下比对，**绝不整文件覆盖**。
- 发现 `fs.Verb == "review"` 之外还有别的调用方也会置位 → 停下报告，改设计（改用显式
  `fanoutState` 布尔字段）。
- 改 Step 3 后 `./pkg/tui` 出现与本 bug 无关的既有测试失败 → 按 owner 裁决定位根因修掉并补记；
  若属方案取舍则停下问。
- 真机验收发现卡片仍不在两条确认行之间（说明落点假设不成立）→ 保留证据停下报告，不叠加补丁。

## 9. Maintenance notes

- 本修复把评审卡片归一到既有「审批交换 = 最后一个工具块之前」的布局约定
  （`retainFrameLocked` :4972 注释所写规则）。以后新增「由审批发起、在审批解决前完成」的
  卡片时，同样走 `replaceOrInsertBeforeLastTool` + `InsertBeforeLastTool` 置位，
  不要另造落点。
- replay 的锚定依赖 plan-review payload 的 `action_id` 与其 approval 事件的
  `tool_step_id`（`pkg/event/approval_events.go:38,74`）。若将来计划评审事件改成不再携带
  `action_id`，replay 锚定会失效——那时需要在 payload 上补 `tool_step_id`。
- 评审者：重点看 `retainFrameLocked` 分支是否只对 `Verb=="review"` 生效（不得影响普通
  subagent 卡片的尾部追加语义），以及 live/replay 两个测试是否真的在改动前会失败。

## 10. Steps 落地状态

- [x] Step 1 `viewModel.replaceOrInsertBeforeLastTool`（`pkg/tui/tty_session.go`，紧接 `replaceOrAppendBlock` 之后）。
- [x] Step 2 `emitFanoutFrame` 置 `InsertBeforeLastTool: fs.Verb == "review"` + `Frame.InsertBeforeLastTool` 字段注释改写（`pkg/tui/reducer.go`）。
- [x] Step 3 `retainFrameLocked` 按标记路由到 `replaceOrInsertBeforeLastTool`（`pkg/tui/reducer.go`）。
- [x] Step 4 replay 锚定：新增 `planReviewAnchorByAction` / `planReviewEventActionID`，`replayTimelineWithReducer` 用它给 `plan_review_started/reviewed` 补落点（`pkg/tui/commands.go`）。
- [x] Step 5 live + replay 两个回归测试（`pkg/tui/reducer_test.go`、`pkg/tui/commands_test.go`）。
- [x] Step 6 全量 build/test/vet + tmux 真机（live 与 resume replay 双路径）。

## Implementation Status

### What landed

| 文件 | 改动 |
|---|---|
| `pkg/tui/tty_session.go` | 新增 `(*viewModel).replaceOrInsertBeforeLastTool`：同 `Kind`+`StepID` 已存在则原位更新，否则 `insertBeforeLast(prev.Kind == FrameTool)`。 |
| `pkg/tui/reducer.go` | `emitFanoutFrame` 增 `InsertBeforeLastTool: fs.Verb == "review"`；`Frame.InsertBeforeLastTool` 注释改为「approval-exchange 帧（确认行 + 它请来的 plan-review 卡片）」；`retainFrameLocked` 的 fanout/tool 分支内按该标记路由（`FrameFanout && InsertBeforeLastTool` → `replaceOrInsertBeforeLastTool`，其余不变）。 |
| `pkg/tui/commands.go` | 新增 `planReviewAnchorByAction`（approval action → 该 action 命名的工具行 `+1`，只取首个命中）与 `planReviewEventActionID`（plan-review 事件 → `action_id`）；`replayTimelineWithReducer` 建 `reviewAnchors`，在 `approvalEventAnchorPosition` 返回 -1 后、`anchors`/时钟回退之前用它补 `pos`。 |
| `pkg/tui/reducer_test.go` | 新 `TestPlanReviewCardLandsBetweenTheApprovalConfirmations`。 |
| `pkg/tui/commands_test.go` | 新 `TestReplayTimelinePutsThePlanReviewCardBetweenTheApprovalLines`。 |
| `docs/plan/PLAN_REVIEW_CARD_ORDERING_PLAN.md` | 本计划落盘。 |

全部改动留在工作区，**未 commit**。`reducer.go`/`reducer_test.go`/`tty_session.go` 在本会话前是干净文件，
其 `git diff` 只有本修复；`commands.go`/`commands_test.go` 本就带 owner 未提交改动，只做手术式 `edit_file`，未整文件覆盖。

### Deviations from the plan

1. **计划 §3 曾考虑「用显式 `fanoutState` 布尔字段」作备选（STOP condition 2）**，未触发：`grep` 确认
   `Verb: "review"` 只有 `openPlanReviewCard` 一处赋值，`InsertBeforeLastTool: true` 只有
   `render.go:753`、`commands.go:575` 两处（都是 `FrameStatus` 审批确认行），因此复用 `fs.Verb == "review"`
   作为标记是安全的。实现与计划一致，未改设计。
2. **计划 §8 STOP condition 3（既有测试失败）未出现**：`./pkg/tui` 全量包级测试（含 `-tags fts5`）41.2s 全绿。
3. **`gofmt` 一处收尾**：`emitFanoutFrame` 新增带注释的字段后，后续字段的对齐组被 gofmt 要求重排
   （同一 struct literal 内 7 行缩进）；按整文件 `gofmt` 的**唯一** diff hunk 做了等价的 `edit_file` 手术式对齐，
   未整文件覆盖。随后 `gofmt -l` 对五个改动文件均无输出。
4. **真机验收路径与计划 §6 描述略有出入（等价且更严格）**：计划建议临时 `FOREBRAIN_HOME` + 临时项目目录；
   实际使用仓库自带的 `.claude/skills/run-forebrain/driver.sh`（隔离 `$TMPDIR/forebrain-run` 下的 home 与项目目录，
   同样不碰真实 `~/.forebrain`），provider 用 fake 的 `toolcall` 模式（真实审批浮层 + 真实 plan review 派发）。
   计划里「配置一个可用 provider」由 driver 的 deepseek 假 provider 满足。
5. **计划未预见的一点真机现象（非缺陷，未处理）**：`driver.sh start` 的就绪探针等的是 `/ commands`，
   而 resume 启动时该 hint 位置显示的是 `resumed N messages above; scroll up to review them.`，
   所以 `RESUME_ID=... $D start` 会报「TUI never reached the composer」。app 实际已正常起来，
   `$D screen` 取证成立——这是 driver 脚本自身的探针与 resume banner 不匹配，**不属于本修复范围**，未顺手改。

### Verification

**改动前失败证明（把两处决定性改动临时置为 `false`，只留测试）**

```
--- FAIL: TestReplayTimelinePutsThePlanReviewCardBetweenTheApprovalLines
    commands_test.go:1871: the card must replay between the approval's two confirmation lines, above the call it names: [asked approved exit plan mode card:plan-review:rv-1]
--- FAIL: TestPlanReviewCardLandsBetweenTheApprovalConfirmations
    reducer_test.go:2326: transcript order:
        [assistant status:✔ You asked zhipuai/glm-5.3-flash to review the plan status:✔ You approved forebrain to exit plan mode exit plan mode card:rv-1]
        want:
        [assistant status:✔ You asked zhipuai/glm-5.3-flash to review the plan card:rv-1 status:✔ You approved forebrain to exit plan mode exit plan mode]
```

两个测试在改动前确为红，且失败形态与 §2.2 的块序列推演一致（卡片排在 `approved` 之后、末尾）。

**全量命令**

| 命令 | 结果 |
|---|---|
| `CGO_ENABLED=1 go build ./...` | exit 0 |
| `CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -run 'PlanReview\|ReplayTimeline' -count=1` | ok |
| `CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -count=1` | ok 41.225s |
| `go vet ./...` | exit 0 |
| `gofmt -l`（五个改动文件） | 无输出 |
| driver `cmd_build`（= `go build -tags fts5 -o … ./cmd/forebrain`） | 成功 |

**tmux 真机（driver.sh，`$TMPDIR/forebrain-run`，`start toolcall`）**

流程：`submit 'please write a short plan'` → enter_plan_mode 浮层 Enter → exit_plan_mode 浮层按 `4`+Enter 进
reviewer 选择 → Enter 请求评审 → 浮层重开后再按 `2`+Enter 批准退出计划。

live（`$D screen`）：

```
● calling exit_plan_mode

✔ You asked deepseek / deepseek-chat to review the plan

● Failed to start 1 plan-reviewer task
    no plan to review
  └ ✗ Plan review

✔ You approved forebrain to exit plan mode

◆ Exited plan mode <0.1s …
```

resume replay（`RESUME_ID=cli-4f8ccb1f-… $D start toolcall`，同一 session）：

```
● calling exit_plan_mode

✔ You asked deepseek / deepseek-chat to review the plan

● Failed to start 1 plan-reviewer task
    no plan to review
  └ ✗ Plan review

✔ You approved forebrain to exit plan mode

◆ Exited plan mode <0.1s
```

两路径逐块一致（live/replay 1:1），卡片都夹在两行确认之间、且都在它所属工具块之上。

**真机证据的边界说明（诚实记录）**：假 provider 的第二轮是 `exit_plan_mode`，评审请求答的是纯文本
`FAKE_ANSWER`，而 fake 项目目录里没有 plan 文件，因此评审以 `Failed to start 1 plan-reviewer task / no plan to review`
（`plan_reviewed{outcome:failed}`）收场。**这不影响本次验收**：卡片由 `plan_review_started` 的 `FrameFanout`
建立（`retainFrameLocked` 的落点就在这里发生），`plan_reviewed` 只做同 StepID 原位更新——两帧都真实经过了被测路径。
但「评审成功（`outcome=done`）时卡片同样落位」只由单测（`Ran 1 plan-reviewer task` 断言）覆盖，未在真机复现。

**事件证据**（`fb_session_events`，与计划 §2.1 的真实 session 同形）：

```
10 approval_requested  action_id=13265b07-… tool_step_id=call_fake_1   (exit_plan_mode)
11 approval_resolved   decision=review_requested
12 plan_review_started action_id=13265b07-…
13 plan_reviewed       action_id=13265b07-… error="no plan to review"
15 approval_resolved   decision=approved
```

### Not done

- 未 commit（符合默认禁令）；未 push。
- 未新增 `docs/plan/` 之外的文档，未改 `pkg/gateway/**` 与 web 前端（§4 Out of scope：web 的评审在 approval 卡片体内呈现，无 transcript 卡片）。
- 未顺手修 `driver.sh` 的 resume 就绪探针（见 Deviations 5）。

