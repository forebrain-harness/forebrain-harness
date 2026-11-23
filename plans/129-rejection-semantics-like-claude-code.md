# Plan 129：拒绝审批的语义与 Claude Code 一致——不带说明的拒绝中止回合，带说明的拒绝只拒这一个调用、回合继续；“No”行可直接写说明

> **执行者须知**：按步骤顺序执行，每步跑验证命令并确认预期结果后再进下一步。触发
> "STOP conditions" 时停下报告，不要即兴发挥。**不要更新 `plans/README.md`**（评审方维护索引）。
> **禁止提交代码**（owner 手动提交）：不 commit、不 push、不开 PR、不建分支，改动直接留在 dev 工作区。
> 工作区里若有与本计划无关的未提交改动，**不要碰、不要回滚、不要格式化**。
>
> **前置（否则 STOP 1）**：`grep -n "func DeniedToolStep" pkg/tool/format.go`、`grep -n "func buildDenialMessage" pkg/run/orchestration_llm.go`、`grep -n "func (o \*approvalOverlay) dismissDecision" pkg/tui/overlays.go`、
> `grep -n "func (s \*ChatSession) abortPendingApproval" pkg/tui/chat_turn.go`、`grep -n "func (s \*Server) abortGatewayRunForAction" pkg/gateway/approval.go` 各有结果。本计划不依赖 122–127，可以在它们之前执行；`127` 依赖本计划。
>
> **Drift check**：按下面摘录里的符号定位；锚点找不到或内容不符即 STOP 1。

## Status

- **Priority**: P1
- **Effort**: M–L
- **Risk**: MED（改变拒绝后的走向：不带说明的拒绝从“回合照常继续/取消”统一为“写拒绝结果并中止回合”；带说明的拒绝不再连带拒绝同一回答的其他调用）
- **Depends on**: —（Part B 的 web 实施等原型确认）
- **Category**: direction（owner 2026-10-25：拒绝审批之后改成 Claude Code 的做法）
- **Planned at**: dev `c72da06`，2026-10-25

## Why this matters

Claude Code（2.1.301 二进制，权限上下文的 `cancelAndAbort(feedback, isAbort)`）对用户拒绝的处理：

| 情况 | 被拒调用的结果（给模型） | 之后 |
|---|---|---|
| 主代理，拒绝**带说明** | `The user doesn't want to proceed with this tool use. The tool use was rejected (eg. if it was a file edit, the new_string was NOT written to the file). To tell you how to proceed, the user said:\n<说明>` | 回合继续；同一回答里的其他调用照常执行 |
| 主代理，拒绝**不带说明**（“No”空着提交、Esc、Ctrl+C） | `The user doesn't want to proceed with this tool use. The tool use was rejected (eg. if it was a file edit, the new_string was NOT written to the file). STOP what you are doing and wait for the user to tell you how to proceed.` | **回合中止**：还没开始的调用得到同一句结果，正在执行的被中断，等用户开口 |
| 子代理，拒绝带说明 | `Permission for this tool use was denied. The tool use was rejected (eg. if it was a file edit, the new_string was NOT written to the file). The user said:\n<说明>` | 子代理继续 |
| 子代理，“No”空着提交 | `Permission for this tool use was denied. The tool use was rejected (eg. if it was a file edit, the new_string was NOT written to the file). Try a different approach or report the limitation to complete your task.` | 子代理继续 |
| 子代理，Esc / Ctrl+C | （中止该子代理） | 子代理结束 |

审批框里，“No”那一行本身是输入框（占位 `and tell Claude what to do differently`）：直接回车 = 不带说明，写了再回车 = 带说明；Esc 在任何位置都是不带说明的拒绝。`exit_plan_mode` 的 “No, keep planning” 也是输入框，但**必须写说明**（空着回车不做任何事）；Esc 是不带说明的拒绝（中止）。

forebrain 现在：TUI 的拒绝选项是 codex 式的两种——“No, and tell Forebrain Harness what to do differently”（`Cancelled`：取消整个运行，被拒调用没有结果行，事后被 `RepairDanglingToolResults` 连 assistant 调用一起剥掉）与部分请求的 “No, continue without …”（`Denied`）；续跑时的拒绝把**同一回答里所有缺结果的调用**都拒掉，文案是 `Tool approval denied by user: …`。本计划改成上表。

保留不动：forebrain 独有、Claude Code 没有对应物的“继续但不授予”选项（`DecisionDecline`：`request_permissions` 的 “No, continue without permissions”、记忆笔记/读取），它们是“拒绝但继续”的答复，模型收到子代理那句“Try a different approach…”的文案；`exit_plan_mode` 带说明拒绝时给模型的文字仍是 forebrain 组合的计划指导（含评审意见），只是改为必须带说明；网络放行的提问；审批过期。

## Current state

- `pkg/run/orchestration_llm.go`：
  - `func buildDenialMessage(toolName, denyReason string) string`：`"Tool approval denied by user: the user rejected this %s tool call. Try a different approach or ask the user for guidance."` + 可选 `"\n\nUser feedback: " + reason`。
  - 续跑入口 `resumeAssistant, ok := w.consumeResumeSnapshot(ctx, messages)`；`if resumeAssistant.denied {` 对 `resumeAssistant.pendingMsg.ToolCalls` 里**每个**没有结果的调用写 `llm.ToolResultMessage(tc.ID, llm.Text(buildDenialMessage(...)))`，带 `tool.DeniedToolStep(...)` 的显示并 `step(ctx, refused)`（`deliveredReview` 分支写 `PlanReviewDeliveredDisplayKey`）；否则 `replayPendingToolCalls`（缺结果的第一个调用带批准 ID 执行，其余 `executeToolCalls`；执行前 `tool.BeginApprovalContinuation(ctx)`）；之后 `tool.EndApprovalContinuation(ctx)`。
  - `func steerPreemptedResult(index int, call llm.ToolCall) orchestratedToolResult`（`display: &llm.ToolDisplayState{Body: tool.SteerPreemptedDisplayKey, ToolMetaJSON: {"tool_name","status":"not_run"}}`, `notRun: true`）。
- `pkg/tool/format.go`：`PlanReviewDeliveredDisplayKey`、`SteerPreemptedDisplayKey`、`DeniedToolDisplayBody(toolName, feedback)`、`DeniedToolStep(stepID, toolName, input, feedback)`。
  `pkg/tui/commands.go` `func isHiddenToolDisplay(body string) bool`（两个 key 整行不画）；web `useChatStream.ts` `STEER_PREEMPTED_KEY` 与 `function isSteerPreemptedRow(row)`。
- `pkg/turn/approval.go`：`ApprovalResume{Session, Approved, Denied, Reason, Feedback, DeliveredReview, Cleared}`、`BuildApprovalResume(action, snapshot, toolName, cleared)`（`Denied` 来自 `action.Status == state.ActionDenied`，`Reason` 来自 `action.Error`）；`tool.ToolApprovalResumeState{Session, Denied, DenyReason, DenyFeedback, …}`。
- TUI `pkg/tui/overlays.go`：`buildApprovalChoices`（`safety.DecisionCancel` → `overlayChoice{label: "No, and tell Forebrain Harness what to do differently", cancel: true}`；web search/fetch 时标签为 `No, don't search` / `No, don't fetch`；`DecisionDecline` → `denyOnce`）；`buildExitPlanChoices`（`{label: "No, keep planning", denyOnce: true, isPlanDeny: true}`）；
  `commit()`（`denyOnce` → `Denied`，`cancel` → `Cancelled`）；按键：Esc → `o.dismissDecision()`（有 cancel 选项 → `Cancelled`；只有 decline → `Denied`）、Ctrl+C → `Cancelled`、`n` → `denyChoiceIndex` 后 `commit()`；
  计划拒绝的说明输入：`enterPlanDenyMode()`、`handlePlanDenyKey`（Esc 回到选项列表、Ctrl+C `Cancelled`、Enter → `Denied{DenyReason: feedback}`，空说明也提交）。
- TUI 取消：`chat_surface.go` `completeSurfaceApproval` 在 `result.Cancelled` 时调 `s.abortPendingApproval(act.SessionID, id)`；`chat_turn.go` `abortPendingApproval` 取消该运行并走结束流程（被拒调用不写结果）。
- gateway 取消：`pkg/gateway/approval.go` `abortGatewayRunForAction(action)`（`network_inline` 除外）：`runController().Cancel`、`ClearWait`、`turn.FinalizeCancel`、`finishRun(... RunStatusCancelled)`；调用点在 `api_extra.go` 的审批决定处理里（三处）。
- 落库：`pkg/state/session_store.go` `AppendMessageSequenceForRun`、`RepairDanglingToolResults`（剥掉没有结果的 assistant 调用行）；等待行 `fb_run_waits.session_snapshot_json`（停回合时已写入快照）。
- web：`frontend/src/components/chat/ApprovalCard.vue`（普通审批按钮由 `approvalDecisionOptions(a)` 生成，`submitApprovalDecision(a, option)` 直接提交；exit_plan 的 “Keep planning” 按钮先展开说明输入框 `data-testid="exit-plan-feedback"` 再提交）；`frontend/src/composables/usePendingActions.ts`（`forebrainApi.actionsApprove(action.id, { reason, decision, … })`）。
  原型稿：`docs/plan/WEB_REDESIGN_AND_GATEWAY_ACCESS/app-preview.html`（owner 规则：web 改动先在原型上迭代、确认后 1:1 实施）。

## Commands you will need

| Purpose | Command | Expected on success |
|---|---|---|
| Go 测试 | `export PKG_CONFIG_PATH="$PWD/third_party/pdfium/$(go env GOOS)_$(go env GOARCH)" FOREBRAIN_HOME="$(mktemp -d)"; CGO_ENABLED=1 go test -tags fts5 ./pkg/tool/... ./pkg/turn/... ./pkg/run/... ./pkg/tui/... ./pkg/gateway/... ./pkg/state/... -count=1` | 全部 `ok` |
| vet / fmt | `CGO_ENABLED=1 go vet -tags fts5 ./pkg/...`；`gofmt -l pkg/` | 无输出 |
| 前端 | `cd frontend && pnpm vitest run && pnpm typecheck` | 通过 / exit 0 |
| 真机 | `.claude/skills/run-forebrain/driver.sh`（Step 7） | 见期望 |

## Scope

**In scope**：`pkg/tool/format.go`(+test)、`pkg/run/orchestration_llm.go`(+test)、`pkg/turn/approval.go`(+test)、新建 `pkg/turn/rejection.go`(+test)、`pkg/tui/overlays.go`、`pkg/tui/chat_surface.go`、`pkg/tui/chat_turn.go`、`pkg/tui/commands.go`、TUI 测试、
`pkg/gateway/approval.go`、`pkg/gateway/api_extra.go`（取消处理三处）、gateway 测试、`frontend/src/composables/useChatStream.ts`（隐藏行判断一处）；Part B：`docs/plan/WEB_REDESIGN_AND_GATEWAY_ACCESS/app-preview.html`，确认后 `ApprovalCard.vue`、`usePendingActions.ts`、`frontend/src/locales/index.ts` 及其测试。

**Out of scope**：网络放行提问；提问工具（`user_interaction`）；审批过期；`DecisionDecline` 选项本身（保留）；计划评审流程；当场审批（`127` 会接上本计划的语义）。

## Steps

### Part A：引擎与 TUI

#### Step 1：拒绝结果的文字与隐藏键（`pkg/tool/format.go`）

```go
const (
	// RejectedStopText is what a primary agent's call refused without words
	// receives: the turn stops here and waits for the user (Claude Code's text).
	RejectedStopText = "The user doesn't want to proceed with this tool use. The tool use was rejected (eg. if it was a file edit, the new_string was NOT written to the file). STOP what you are doing and wait for the user to tell you how to proceed."
	rejectedWithFeedbackPrefix = "The user doesn't want to proceed with this tool use. The tool use was rejected (eg. if it was a file edit, the new_string was NOT written to the file). To tell you how to proceed, the user said:\n"
	deniedText                 = "Permission for this tool use was denied. The tool use was rejected (eg. if it was a file edit, the new_string was NOT written to the file). Try a different approach or report the limitation to complete your task."
	deniedWithFeedbackPrefix   = "Permission for this tool use was denied. The tool use was rejected (eg. if it was a file edit, the new_string was NOT written to the file). The user said:\n"
)

// RejectionText is what a refused call's result tells the model. subagent is
// a refusal of a subagent's own call; feedback is the user's words.
//   primary + words → rejectedWithFeedbackPrefix + words
//   primary, none   → deniedText (only a decline option reaches this: a
//                     refusal without words otherwise stops the turn and
//                     uses RejectedStopText)
//   subagent + words → deniedWithFeedbackPrefix + words
//   subagent, none   → deniedText
func RejectionText(subagent bool, feedback string) string

// RejectionStoppedDisplayKey is the display body of a call the run never
// started because a refusal without words stopped the turn. No surface
// renders it, like SteerPreemptedDisplayKey.
const RejectionStoppedDisplayKey = "Not run — the user refused a call in this response and stopped the turn."
```
测试：五种组合的文字逐字断言。

#### Step 2：续跑时的拒绝只拒被问的那个调用（`orchestration_llm.go`）

1. `buildDenialMessage` 删除，调用点改用 `tool.RejectionText(isSubagentRun, reason)`。`isSubagentRun`：本编排实例跑的是子代理（用编排层现有的判断方式；找不到现成判断就用 `tool.HookAgentIDFromContext(ctx) != ""`，两者都不可靠即 STOP 3）。`exit_plan_mode` 调用保持现有文字（`resumeAssistant.denyReason` 是组合好的计划指导），即 `tc.Function.Name == "exit_plan_mode"` 时文字为 `denyReason` 原样（为空时用 `tool.RejectionText(...)`）。
2. 续跑拒绝分支改为：缺结果的调用里**第一个**（被问的那个——与 `replayPendingToolCalls` “The approval belongs to the first pending call alone” 同一判定）得到拒绝结果（文字见 1，显示照旧用 `DeniedToolStep`，`deliveredReview` 分支照旧）；**其余缺结果的调用照常执行**：照 `replayPendingToolCalls` 的写法（执行前 `tool.BeginApprovalContinuation(ctx)`，`withForkRuntimeSnapshot`，不带批准 ID，`w.executeToolCalls(toolCtx, rest, toolMap)`，再次出现 gate 时照该函数的处理附快照返回），结果按调用顺序接在会话里。
3. 新增“拒绝并中止”的续跑：`tool.ToolApprovalResumeState` 加 `Stopped bool`（注释：the user refused without words; record the refusal and end the turn）；`consumeResumeSnapshot` 带出它；入口处 `if resumeAssistant.stopped {`：第一个缺结果的调用得到 `tool.RejectedStopText`（显示 `DeniedToolStep`，`step(ctx, refused)`），其余缺结果的调用得到 `tool.RejectedStopText` 加隐藏显示（照 `steerPreemptedResult` 新增 `rejectionStoppedResult(index, call)`，`Body: tool.RejectionStoppedDisplayKey`、`status: "not_run"`，不发步骤事件）；`partialCapture.Set(session)`、`transcript.flush(...)`、`tool.EndApprovalContinuation(ctx)`，然后 `return nil, ErrTurnStoppedByRejection`（新哨兵，`pkg/run` 导出：`var ErrTurnStoppedByRejection = errors.New("the user refused a tool call and stopped the turn")`），**不发模型请求**。
4. 测试（`orchestration_llm_test.go`，照现有续跑拒绝测试）：带说明拒绝 → 第一个调用文字为 `To tell you how to proceed, the user said:\n<words>`、第二个调用被执行；`Stopped` → 两个调用都是 `RejectedStopText`、第二个的显示为隐藏键、inner 没有被调用、返回 `ErrTurnStoppedByRejection`；子代理拒绝无说明 → `Try a different approach…`；`exit_plan_mode` 拒绝文字不变。

#### Step 3：拒绝并中止的落库入口（`pkg/turn/rejection.go`）

```go
// ResumeRejectedStop is the resume state for a gate the user refused without
// words: the refusal is recorded on the parked call and the turn ends there.
func ResumeRejectedStop(snapshot []llm.Message) *tool.ToolApprovalResumeState // Session: snapshot, Stopped: true
```
TUI 与 gateway 在用户“不带说明拒绝”主代理的停回合审批时，**不再直接取消运行**，而是用这个续跑状态跑一次续跑（与现有“批准 → 续跑”“拒绝 → 续跑”同一条路，只是续跑在写完拒绝结果后以 `ErrTurnStoppedByRejection` 结束），续跑结束后按“取消”收尾（运行状态 cancelled、两端照取消显示）。
- `ErrTurnStoppedByRejection` 在两端的续跑结果处理里按用户取消对待：不出错误卡、运行结束为 cancelled（找到现有处理 `context.Canceled` 的分支，并列加它）。
- 子代理的停回合审批（`rae.AgentID != ""`）、`network_inline`、审批过期：保持现有取消行为。

#### Step 4：TUI 接入（`chat_surface.go`、`chat_turn.go`）

1. `completeSurfaceApproval`：`result.Cancelled` 且该审批是主代理自己的停回合 gate（`turn.ActionSubagent(result.Action)` 为空，且不是 `network_inline`）时，不调 `abortPendingApproval`，改走“拒绝并中止”的续跑：照 `resumeAfterDenial` 的写法新增 `resumeAfterRejectedStop(actionID)`，续跑上下文用 `turn.ResumeRejectedStop(p.SessionSnapshot)`（快照来自 `takePendingApprovalForAction`/等待行，与拒绝续跑相同的来源）；`retryResumeDispatch` 的 `cancel` 分支同样改走它（幂等重试时）。
2. `abortPendingApproval` 保留给子代理与其他取消来源。
3. 测试（照现有停回合审批的 TUI 测试与假 sink）：主代理 gate 用户“不带说明拒绝” → 库里该调用有结果行（`RejectedStopText`），assistant 调用行没被 `RepairDanglingToolResults` 剥掉，运行 cancelled，没有新的模型请求；带说明拒绝 → 回合继续、同一回答的其他调用被执行。

#### Step 5：gateway 接入（`approval.go`、`api_extra.go`）

`abortGatewayRunForAction` 的三个调用点：对主代理自己的停回合 gate（`turn.ActionSubagent(action)` 为空、非 `network_inline`、有等待行且有快照），改为分派一次“拒绝并中止”的续跑（照批准/拒绝续跑的分派函数写法，续跑上下文 `turn.ResumeRejectedStop(wait.SessionSnapshot)`）；续跑以 `ErrTurnStoppedByRejection` 结束后按现有取消收尾（`ClearWait`、`FinalizeCancel`、`finishRun(... cancelled)`、发 `turn_cancelled`）。没有快照的旧等待行 → 仍按现有直接取消（兼容）。
测试：照现有 gateway 审批取消测试——主代理 gate 被取消 → 库里该调用有 `RejectedStopText` 结果、运行 cancelled、没有模型请求；子代理 gate 取消行为不变。

#### Step 6：TUI 审批框（`overlays.go`）

1. `buildApprovalChoices`：`DecisionCancel` 的那一行改为输入行：`overlayChoice{label: "No", cancel: true, feedbackInput: true}`，输入占位 `and tell Forebrain Harness what to do differently`（web search/fetch 的专用标签一并改为 `No`）；没有任何选项时的兜底同样改。`DecisionDecline` 保持原标签与 `denyOnce`。
2. 选中 `feedbackInput` 行（回车、`n` 键、数字键选中后回车）进入说明编辑（复用 `planDenyEditing` 的输入、光标、粘贴、换行与渲染机制，抽成两者共用的 `feedbackEditing` 状态与 `handleFeedbackKey`）：
   - Enter：说明非空 → `Denied{DenyReason: 说明}`；说明为空 → 主代理请求（`req.AgentID == ""`）`Cancelled`，子代理请求 `Denied{DenyReason: ""}`。
   - Esc：主代理请求 → `Cancelled`；子代理请求 → `Cancelled`（中止该子代理，现有行为）。
   - Ctrl+C：`Cancelled`（不变）。
   - 其余编辑键与计划拒绝说明一致。
3. 选项列表上的 Esc（`dismissDecision`）：有 cancel 选项 → `Cancelled`（不变）；只有 decline 的请求 → 不变。
4. `buildExitPlanChoices` 的 “No, keep planning”：Enter 时说明为空 → 不提交、留在编辑态（显示不变）；说明非空 → `Denied{DenyReason}`（不变）。编辑态里的 Esc 改为 `Cancelled`（与 Claude Code 一致：Esc 就是不带说明的拒绝，中止回合）；帮助行文字随之更新。
5. 测试（`pkg/tui` 现有审批框测试的写法）：选 “No” 直接回车 → `Cancelled`；输入 `use npm` 回车 → `Denied{DenyReason: "use npm"}`；子代理请求直接回车 → `Denied{DenyReason: ""}`；Esc → `Cancelled`；keep planning 空回车不提交、有说明提交、编辑态 Esc → `Cancelled`；“No” 行的渲染（标签 `No`，选中时显示占位文字）照现有快照测试方式断言。

#### Step 7：隐藏行与真机验收

1. `pkg/tui/commands.go` `isHiddenToolDisplay` 加 `tool.RejectionStoppedDisplayKey`；web `useChatStream.ts` 加 `REJECTION_STOPPED_KEY`（与 Go 常量逐字相同），`isSteerPreemptedRow` 改名为 `isHiddenRunRow` 并认两个键（调用点同步），加单测。
2. 真机（TUI，run-forebrain；`multishell` 模式现有三条 shell 调用，用 `FAKE_SHELL_ESCALATE=1` 开关让第一条带 `sandbox_permissions: require_escalated` 以触发审批——若该模式没有可加开关的位置，就新增模式 `rejectstop`，脚本同上）：

```bash
D=.claude/skills/run-forebrain/driver.sh
FAKE_SHELL_ESCALATE=1 $D start multishell
$D submit 'run them'; $D wait 'Yes, proceed' 30   # 审批框出现
$D key n; $D key Enter          # 选 No，不写说明直接回车
$D wait 'Worked for|Cancel|Interrupt' 20
$D screen      # 期望：被拒调用一张拒绝卡；另外两条命令没有卡；回合结束，等输入
$D messages    # 期望：assistant 行（3 个调用）、3 条 tool 结果（第 1 条 STOP… 文字，后 2 条同文字且显示为隐藏键）；没有之后的 assistant 回答
$D stop
FAKE_SHELL_ESCALATE=1 $D start multishell
$D submit 'run them'; $D wait 'Yes, proceed' 30   # 审批框出现
$D key n; $D send 'use echo instead'; $D key Enter
$D wait 'Worked for' 40
$D screen      # 期望：第 1 条拒绝卡显示说明；第 2、3 条命令照常执行；回合继续到结束
$D stop
```

**Verify（Part A）**：Go 测试全部 `ok`；`go vet`、`gofmt` 干净；前端 `pnpm vitest run src/composables/useChatStream.test.ts` 与 `pnpm typecheck` 通过；真机两段期望满足，`screen`/`messages` 附在报告里。

### Part B：web 审批卡（先原型）

#### Step 8：原型

在 `docs/plan/WEB_REDESIGN_AND_GATEWAY_ACCESS/app-preview.html` 的审批卡上画：普通审批的拒绝按钮改为 “No”，点后在卡内展开说明输入框（与现有 “Keep planning” 的说明框同一样式），占位 `and tell Forebrain Harness what to do differently`，旁边一个发送按钮；空着发送 = 拒绝并中止，写了发送 = 带说明拒绝；Esc 收起并按不带说明拒绝提交。exit_plan 卡的 “Keep planning” 发送按钮在说明为空时置灰。
画完 **STOP**，把原型截图/链接交给 owner 确认；确认前不做 Step 9。

**2026-10-25 评审重画（以原型为准，取代上面一段的画法；语义不变；Step 9 由 `plans/138-web-approvals-on-the-waiting-card.md` 取代）**：入口「对话 · 审批卡（全部工具）」「对话 · 计划审批 ①–⑥」。
- 等你批准的调用是活动线上的一张普通工具卡：节点是时钟，卡头是引擎的进行时卡头（`DisplayParts`，中文界面显示中文：正在写入 / 正在运行 / 正在申请权限 …）；不再是“旧式调用行 + 单独的审批框”。
- 面板只放决定要看的事实（行数、diff、命令会做什么、主机、参数）；问题与回答在卡底部的浅品牌色底栏（`.gate-ask`），等待中的卡边框用品牌色。
- **拒绝按引擎的决定分两族**（`safety.BuildApprovalDecisionOptions`）：
  - cancel 一族（写文件、改文件、补丁、命令、外网、取页面、搜索、MCP、进入计划模式、兜底）：拒绝按钮保留自己的动词（不要写 / 不要运行 / 不要取 …，不是一个“不”字），点开说明框（占位“告诉它该怎么做（可不填）”）；发送按钮的字随内容变——空着是「拒绝并停下」（= `decision: 'cancel'`，回合中止），有字是「发送」（= `decision: 'deny'` + `reason`，只拒这一次、回合继续）；Esc = 拒绝并停下；点任何“允许”类按钮收起说明框。
  - decline 一族（`request_permissions` 的「不授予」、记忆工具的「不要记」）：按钮直接提交 `decline`，回合继续，**没有说明框**（上一稿把「不授予」做成了说明框，与引擎不符）。
- exit_plan：**不在对话里画卡**（owner：终端不画卡，web 就不画卡），等待时计划与回答**占输入框的位置**（头一行“计划 + 文件名 + 眼睛”、计划正文、同一种底栏）；
  「继续规划」展开同一种说明框，说明必填（空时发送置灰），旁边提示 `Esc 拒绝并停下`；去掉单独的「取消」按钮（再点「继续规划」收起）；批准后输入框回来，对话里落一张“已退出计划模式”。
- 答完之后：写了说明 → 卡收成“已拒绝 …”，一句“你拒绝了，文件没有写入。”下面是用户的说明，回合继续；空着拒绝或 Esc → 卡收成“已拒绝 …”，同一回答里未开始的调用不出卡，“已工作 …”收尾。
- 不画：说明框下的长提示句、本轮新增按钮的蓝底圆点标注（评审标注已删）。

#### Step 9：实施（owner 确认后）

1:1 还原原型（以上面「评审重画」为准）：`ApprovalCard.vue` 的审批改为挂在工具卡底部的底栏；cancel 决定按钮（保留各族动词文案）改为展开说明框，decline 决定按钮保持直接提交；发送时：说明非空 → `forebrainApi.actionsApprove(id, { decision: 'deny', reason: <说明> })`（与 TUI 的 `Denied{DenyReason}` 同一后端路径——先读 `api_extra.go` 的决定处理确认 `deny` + `reason` 会写进 `action.Error` 并走拒绝续跑；不是就 STOP 4）；说明为空或 Esc → `decision: 'cancel'`。`usePendingActions.ts` 加对应方法；文案进 `locales/index.ts`；exit_plan 空说明禁用发送。单测照 `exit-plan-feedback` 的现有测试写。

**Verify（Part B）**：`pnpm vitest run`、`pnpm typecheck` 通过；e2e（若该卡已有 e2e 用例）通过。

## Done criteria

- [ ] Part A 各步测试通过；`grep -n "Tool approval denied by user" pkg/run/orchestration_llm.go` 无结果；`grep -n "RejectedStopText" pkg/tool/format.go pkg/run/orchestration_llm.go` 各有结果
- [ ] 真机两段符合期望
- [ ] Part B：原型已交 owner；确认后实施并通过前端测试（未确认则 Part B 标为“等原型确认”交回）
- [ ] `git status --short` 只含 Scope 内文件

## STOP conditions

1. 前置不满足或锚点找不到。
2. 现有测试断言“拒绝后同一回答的其他调用也被拒绝”或“取消后调用行被剥掉”——回报用例名，不要为迎合旧断言保留旧语义（owner 已裁决改为 Claude Code 语义，回报后由评审改断言）。
3. 编排层分不清当前是主代理还是子代理的运行。
4. web 的 `deny` + `reason` 走不到拒绝续跑。
5. “拒绝并中止”的续跑需要改动续跑分派的租约/栅栏逻辑本身（而不只是多一种续跑状态）——回报。
6. 某一步验证连续两次修正后仍失败。

## Maintenance notes

- 拒绝的两种走向只由“有没有说明”决定；新增审批界面必须提供“No + 可选说明”，空说明即中止。
- `RejectionStoppedDisplayKey` 与 `SteerPreemptedDisplayKey` 一样是“整行不画”的键，Go 与前端常量必须逐字相同。
- `127`（当场审批）在原处回答时套用同一语义：带说明 → 只拒这一个调用；不带说明 → 写 `RejectedStopText` 并中止回合。

## Execution status（2026-10-26）

**Part A 已实施并验收**（commit `dfb8448`，21 文件）：拒绝文字五组合逐字对齐、`Stopped` 续跑只写拒绝结果不请求模型、
TUI 的 “No” 输入行与 keep-planning 空说明拦截、gateway 三处取消分派改走拒绝并中止、隐藏键两端一致。
真机两段（无说明拒绝 / 带说明拒绝）符合期望。

**Part B 停在 Step 8（等 owner 确认原型）**：原型已画在
`docs/plan/WEB_REDESIGN_AND_GATEWAY_ACCESS/app-preview.html` 的审批卡上 —— 12 张卡的拒绝按钮改为 “No” 输入行
（点开卡内说明框、占位 `and tell Forebrain Harness what to do differently`、旁边 “Refuse” 按钮；空着提交 = 拒绝并中止，
写了提交 = 带说明拒绝，Esc = 不带说明拒绝），exit_plan 卡的发送按钮在说明为空时置灰。
**新增处用原型既有的 fresh 词表做了高亮标注（title=「本轮新增（plan 129）」），属评审标注，1:1 实施时移除。**
Step 9（`ApprovalCard.vue` / `usePendingActions.ts` / `locales`）等 owner 确认后另起。

执行中发现并修掉的一处范围内根因：等待行的 `ToolStepID` 原先用“同名工具取最后一个”的启发式，会把拒绝卡画到
最后一条同名调用上；改为 `pkg/turn.FirstPendingApprovalToolCall`（与引擎“第一个缺结果调用”同一判定）。
