# Plan 148：办公创作类的计划模式——agent 模式不暴露 enter/exit 计划工具、只能由用户显式进入计划模式；退出计划审批改为「执行 / 改计划 / 只要计划 / 审阅」（引擎 + 网关接口 + 终端）

> **执行者须知**：按步骤顺序执行，每步跑验证命令并确认预期结果后再进下一步。触发
> "STOP conditions" 时停下报告，不要即兴发挥。完成后把 `plans/README.md` 里本计划的状态行改为 DONE
> （若评审方派发你并说明由其维护索引，则不改）。
> **禁止提交代码**（owner 手动提交）：不 commit、不 push、不开 PR、不建分支、不建 worktree，改动直接留在 dev 工作区。
> 工作区里与本计划无关的未提交改动**不要碰、不要回滚、不要格式化**。
>
> **前置（否则 STOP 1）**：`145`、`146`、`147` 均为 DONE（本计划用到 `Runner.ProjectType()`、`AgentToolRuntime.ProjectType`、`SessionStore.AddArtifact`、`tool.ClassifyArtifact`、`tool.ProjectTypeFromContext`）。
>
> **Drift check（先跑）**：`git diff --stat d9d0b4d -- pkg/tool/session_tools.go pkg/tool/registry.go pkg/tool/format.go pkg/tool/header_parts.go pkg/run/config.go pkg/run/runner.go pkg/state/mode.go pkg/turn/approval.go pkg/turn/model.go pkg/gateway/api_extra.go pkg/tui/overlays.go pkg/tui/channels.go pkg/tui/chat_surface.go pkg/tui/run.go`
> 有输出是正常的。按"现状"里的函数名核对；含义变了即 STOP 1。
>
> **基线**：`FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/tool ./pkg/run ./pkg/state ./pkg/turn ./pkg/gateway ./pkg/tui -count=1`（900 s）；`cd frontend && pnpm vitest run src/locales/toolHeader.test.ts`。

## Status

- **Priority**: P1
- **Effort**: M–L
- **Risk**: MED（动计划模式的工具表与审批回答；创意编程必须行为不变）
- **Depends on**: `145`、`146`、`147`
- **Category**: direction
- **Planned at**: dev `d9d0b4d` + 2026-10-27 复核工作区

## owner 裁定（2026-10-26）

1. 办公创作类："agent 模式去掉 enter_plan_mode 和 exit_plan_mode 工具，agent 模式不可以自动进入计划模式，只有用户显式切换到 plan 模式才可以开启计划模式，
   且退出计划模式的审批面板里的选项要重新设计，不能直接用创意编程类项目的选项"。
2. 新选项（owner 选定，顺序即显示顺序）：
   ① **按计划执行**；② **修改计划**（必须写修改意见）；③ **只要计划，先不执行**（退出计划模式、本轮结束，计划文件本身就是交付物）；
   ④ **请其他模型审阅**（只在配置了审阅模型时出现）。去掉创意编程的「清空上下文并执行」。
3. 创意编程类保持现状（四个原选项、两个工具照旧）。
4. 跑批类根本没有计划模式（`149` 处理）；本计划只管办公。

## 设计（评审方定，执行者照做）

- **工具表**：办公 runner **不注册** `enter_plan_mode`；注册 `exit_plan_mode`，但每次请求时只在会话处于 plan 模式才把它放进工具表（读会话模式状态，和现有计划模式包装器同一处）。
  模型在 agent 模式下若仍调用 `exit_plan_mode`（幻觉），工具本身拒绝。用户显式进入 plan 模式的途径（TUI 的模式切换 / `/plan`、web 的模式切换）不受影响——它们直接调 `state.Switch`，不经工具。
- **计划模式提醒**：办公有自己的全文提醒（不提代码库），"计划实施提醒"也有办公措辞。
- **「只要计划」的语义**：等同"批准退出计划模式"，但：工具结果告诉模型**不要执行**、只用一两句话告诉用户计划在哪；
  会话模式恢复为进入计划前的模式并记 `PlanKeptOnly=true`，于是"计划实施提醒"不会来催；
  计划文件被登记为一个生成物（`origin='plan'`），用户能下载 / 预览，业务系统能在运行结束帧里拿到。下次进入计划模式时清掉 `PlanKeptOnly`。
- **回答协议**：`turn.ApprovalReply` / 动作 `AnswerJSON` 新增 `plan_only`（与 `clear_context` 并列，规则相同：只对 `exit_plan_mode` 的 accept 合法；与 `clear_context` 互斥）。
- **终端（英文界面）四行**：`Yes, carry out the plan` / `No, revise the plan`（必须写说明，沿用现有 keep planning 的输入行）/ `Keep the plan only, don't carry it out` / `Ask another model to review this plan`（条件出现）。
  卡头：只要计划的那次退出显示新动作词 `Kept the plan`（中文 `已保留计划`）。
- **网页**的四个选项在 `153`（先原型）；本计划只给网关 REST 审批接口加 `plan_only`。

## Current state

### 1. 计划工具（`pkg/tool/session_tools.go`）

- `registerSessionAndMemoryTools(a, st, rt)`（约 `:21-80`）：
  ```go
  t3, err := newEnterPlanModeTool(st, stateRoot, projectKey)
  ...
  t3b, err := newExitPlanModeTool(st, stateRoot, projectKey)
  ...
  registerToolMeta(st, t3, "Use when switching the session into plan mode before implementing a non-trivial task", "session", event.ToolMeta{ReadOnly: true})
  registerToolMeta(st, t3b, "Use when the plan is written and you're ready to leave plan mode for approval", "session", event.ToolMeta{ReadOnly: true})
  ...
  _ = st.Register(a, t3)
  _ = st.Register(a, t3b)
  ```
- `NewPlanModeTools(st, stateRoot)`（约 `:392`，导出给测试）调 `newEnterPlanModeTool` / `newExitPlanModeTool`——**保留签名**。
- `newExitPlanModeTool(st, stateRoot, projectKeys ...string)`（约 `:488-560`）：经 `st.ActionHook()` 要审批；批准后
  `prev, _ := state.Get(stateRoot, sid)` → `restored := prev.PrePlanMode`（空则 agent）→ `state.Switch(stateRoot, sid, restored)` → `st.SetRuntimeSessionMode(...)` →
  返回 JSON `{"mode", "plan_file", "message": "Exited plan mode. You can now make edits, run tools, and take actions. Implement the approved plan in <file>.", "on_completion": "..."}`。
  批准的动作 id 在 `ApprovedActionIDFromContext(ctx)` / `hook` 的返回里；`rt.Actions`（`*state.ActionService`）有 `Get(ctx, id)`。

### 2. 会话模式（`pkg/state/mode.go`）

- `type State struct { Mode; Phase; PrePlanMode; PlanTurnCount; HasExitedPlan bool; UpdatedAt }`，JSON 存在 `<stateRoot>/state/modes/<sid>.json`。
- `Switch(home, sessionID, mode)`：进入 plan 时记 `PrePlanMode`、清 `HasExitedPlan`；离开 plan 时清 `Phase`/`PrePlanMode`、置 `HasExitedPlan=true`。

### 3. 计划模式包装器（`pkg/run/config.go`）

- `wrapPlanModeLLM(inner, stateRoot, projectKeys ...string)`（约 `:724`）构造 `*planModeLLM`；**有 29 处测试直接调用它，不要改签名**。唯一生产调用在 `pkg/run/runner.go` 约 `:1070`：`llmClient = wrapPlanModeLLM(llmClient, r.workspaceRoot(), r.ProjectKey)`。
- `(*planModeLLM).Execute`（约 `:739-797`）每次请求 `state.Get(w.stateRoot, sid)`；非 plan 模式走 `executeImplementationPhase`；plan 模式时算提醒并
  `reminder := buildPlanModeReminder(planDir, planFile, planExists, kind, isReentry, isSubagent)` 注入。
- `buildPlanModeReminder`（约 `:1041-1154`）：subagent 全文以 `"You MUST NOT make any edits to the codebase except to plan files…"` 开头；主代理全文含
  `"## Plan-mode workflow (Phase 1-5)"`、`"Phase 1 - Initial Understanding: explore the codebase…"` 等；sparse 文本（`"Plan mode active. Write/edit plan files only inside %s …"`）本身与代码无关。
- `executeImplementationPhase`（约 `:858-885`）：`!st.HasExitedPlan` → 不提醒；否则读计划文件，没有 `state.ImplementationHeading` 就按节流注入 `buildPlanImplementationReminder(planFile, kind)`（约 `:932-965`，正文提到 "files, packages, commands"、"build, tests"）。
  提醒以常量 `planImplementationReminderMarker = "Plan follow-through required"` 开头（计数靠它）。

### 4. 审批回答（`pkg/turn/approval.go`、`pkg/turn/model.go`）

- `type ApprovalReply struct { Choice; Exec; Network; Permissions; Update; Reason; AskAnswerJSON; ClearContext bool }`（approval.go 约 `:68-77`）。
- `Decide`（约 `:249-325`）：`if reply.ClearContext && (!exit_plan_mode || choice != "accept") { return error "clear context is only valid for an approved exit-plan decision" }`；
  批准时把 `{decision, execpolicy_amendment, network_policy_amendment, update, clear_context}` 序列化进 `AnswerJSON`（`ApproveWithAnswer`）。
- `sameApprovedReply`（约 `:360-378`）比较已批准动作的 `AnswerJSON` 与重放的 reply（幂等）；`decideAsk` 拒绝带 `ClearContext` 的 user_interaction 回答。
- 已批准动作效果的回放（约 `:218-240`）解析同一个匿名 struct（含 `ClearContext`）。
- `ActionClearedContext(action)`（约 `:690-700`）是"从 AnswerJSON 读一个布尔"的范例。
- `type ToolApprovalRequest struct`（model.go 约 `:556-595`）有 `PlanFilePath`、`PlanReviewModels`、`PlanReviews`；`type ToolApprovalDecision struct`（约 `:609-620`）有 `ClearContext bool`、`RequestPlanReview *PlanReviewModelOption`。

### 5. 网关 REST 审批（`pkg/gateway/api_extra.go` 约 `:3715-3765`）

```go
var body struct {
	Reason       string                   `json:"reason"`
	Update       *safety.PermissionUpdate `json:"update,omitempty"`
	ClearContext bool                     `json:"clear_context,omitempty"`
	Decision     string                   `json:"decision,omitempty"`
	...
}
...
err := s.resolveGatewayApproval(r.Context(), id, turn.ApprovalReply{
	Choice: decision, Exec: body.ExecPolicyAmendment, Network: body.NetworkPolicyAmendment,
	Permissions: body.RequestPermissionsResponse, Update: body.Update, Reason: body.Reason, ClearContext: body.ClearContext,
})
```

### 6. 终端（`pkg/tui`）

- `buildOverlayChoices(req, percentUsed)` → `isApprovalExitPlanMode(req)` 时 `buildExitPlanChoices(req, percentUsed)`（`overlays.go` 约 `:209-321`）：
  ```go
  choices := []overlayChoice{
  	{label: clearLabel, allowOnce: true, clearContext: true},
  	{label: "Yes, proceed", allowOnce: true},
  	{label: "No, keep planning", denyOnce: true, isPlanDeny: true},
  }
  if len(req.PlanReviewModels) > 0 {
  	choices = append(choices, overlayChoice{label: "Ask another model to review this plan", isPlanReview: true})
  }
  ```
  `isPlanDeny` 的行是"必须写说明"的输入行（`overlays.go` 约 `:153`、`:916`、`:1205` 的注释）。选中后约 `:1373` 生成 `turn.ToolApprovalDecision{Approved: true, ClearContext: choice.clearContext}`。
- `ApprovalSink`（`channels.go` 约 `:32-60`）字段 `out, rawSel, renderer, recorder, notify`，链式设置器如 `WithRecorder`；`PromptToolApproval`（约 `:74-113`）把 `req` 交给 `newApprovalOverlay`。
  构造在 `pkg/tui/run.go` 约 `:196`：`NewInteractiveApprovalSinkWithTTY(out, selector).WithRenderer(renderer).WithRecorder(opts.Session)`（`opts.Session` 是 `*ChatSession`）。
- 决定落库：`chat_surface.go` 约 `:560-565` 把 `decision.ClearContext` 填进提交结构（`ClearContext: decision.ClearContext`），约 `:620` 处理清空上下文。
- `ChatSession.projectStoreFor()`（`chat_session.go` 约 `:302`）返回 `*state.ProjectStore`。

### 7. 卡头（同 `146` 的机制）

`pkg/tool/header_parts.go` `case lower == "exit_plan_mode":`（约 `:233-245`）：denied → `"Kept planning"`，否则 `"Exited plan mode"`；`HeaderActions()` 手写清单 + `testdata/header_actions.json` golden + 网页中文表
`ai-elements-vue/packages/elements/src/tool/headerWords.ts`（`frontend/src/locales/toolHeader.test.ts` 要求每个英文动作词都有中文）。

## Commands you will need

| 目的 | 命令 | 成功时 |
|------|------|--------|
| 编译 | `go build ./...` | exit 0 |
| 单测 | `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/<pkg> -count=1` | ok |
| golden | `UPDATE_HEADER_ACTIONS=1 CGO_ENABLED=1 go test -tags fts5 ./pkg/tool -run TestHeaderActionsGolden -count=1` | golden 多 1 行 `Kept the plan` |
| 前端词表 | `cd frontend && pnpm vitest run src/locales/toolHeader.test.ts` | passed |
| 终端真机 | 见 Step 8（`.claude/skills/run-forebrain`） | 截屏核对 |

## Scope

**In scope**：`pkg/tool/session_tools.go`、`pkg/tool/session_tools_test.go`、`pkg/tool/format.go`、`pkg/tool/format_test.go`、`pkg/tool/notification.go`、`pkg/tool/notification_test.go`、`pkg/tool/header_parts.go`、`pkg/tool/header_parts_test.go`、`pkg/tool/testdata/header_actions.json`、
`pkg/event/tool_events.go`、`pkg/gateway/wsevents.go`、`pkg/gateway/wsevents_test.go`、`pkg/run/testdata/office_plan_prompts.golden.json`（新建）、
`pkg/state/mode.go`、`pkg/state/mode_test.go`（无则加进该包现有测试文件中与 mode 同名者；若无同名测试文件就新建 `mode_test.go`）、
`pkg/run/config.go`、`pkg/run/config_test.go`、`pkg/run/runner.go`（只换包装器构造那一行）、
`pkg/turn/approval.go`、`pkg/turn/approval_test.go`、`pkg/turn/model.go`、
`pkg/gateway/api_extra.go`（只改 REST 审批 body）、`pkg/gateway/api_extra_test.go`、
`pkg/tui/overlays.go`、`pkg/tui/overlays_test.go`、`pkg/tui/channels.go`、`pkg/tui/chat_surface.go`、`pkg/tui/chat_session.go`、`pkg/tui/run.go`、`pkg/tui/notify.go`、`pkg/tui/notify_test.go`、
`ai-elements-vue/packages/elements/src/tool/headerWords.ts`（只加 1 个词）。

**Out of scope**：网页计划审批面板（`153`）；跑批（`149`）；创意编程的任何文字与选项；`NewPlanModeTools` 与 `wrapPlanModeLLM` 的签名。

## Steps

### Step 1：模式状态记"只要计划"

`pkg/state/mode.go`：`State` 加
```go
// PlanKeptOnly is set when the user approved a plan as the deliverable
// itself ("keep the plan only"): the session left plan mode without carrying
// the plan out, so nothing should nudge it to implement the plan. Entering
// plan mode again clears it.
PlanKeptOnly bool `json:"plan_kept_only,omitempty"`
```
`Switch`：进入 plan 分支里加 `next.PlanKeptOnly = false`。新增
```go
// KeepPlanOnly leaves plan mode the way an approved exit does and records that
// the plan is the deliverable, not work to carry out.
func KeepPlanOnly(home, sessionID string, restore Mode) (State, error)
```
实现：`next, err := Switch(home, sessionID, restore)`；成功则 `next.PlanKeptOnly = true; return next, Set(home, sessionID, next)`。

**Verify**：`go build ./pkg/state` → exit 0。

### Step 2：回答协议加 `plan_only`

1. `pkg/turn/approval.go`：`ApprovalReply` 加 `PlanOnly bool`；`Decide` 里紧接 ClearContext 校验加：
   ```go
   if reply.PlanOnly && (!strings.EqualFold(strings.TrimSpace(act.Kind), "exit_plan_mode") || choice != "accept") {
   	return ApprovalResult{}, fmt.Errorf("plan only is only valid for an approved exit-plan decision")
   }
   if reply.PlanOnly && reply.ClearContext {
   	return ApprovalResult{}, fmt.Errorf("plan only and clear context cannot be combined")
   }
   ```
   批准时序列化的匿名 struct 加 `PlanOnly bool \`json:"plan_only,omitempty"\``；回放效果处与 `sameApprovedReply` 的匿名 struct 加 `PlanOnly bool \`json:"plan_only"\``，比较时加 `answer.PlanOnly == reply.PlanOnly`（无法解析分支加 `&& !reply.PlanOnly`）；
   `decideAsk` 的拒绝条件加 `|| reply.PlanOnly`。新增 `func ActionKeptPlanOnly(action *state.Action) bool`（照 `ActionClearedContext` 写，读 `plan_only`）。
2. `pkg/turn/model.go`：`ToolApprovalDecision` 加 `PlanOnly bool`；`ToolApprovalRequest` 加 `ProjectType string`（注释：会话类型；终端据它选退出计划的选项组；空 = coding）。
3. 终端把决定转成回答的地方（`chat_surface.go` 约 `:560-565`，`ClearContext: decision.ClearContext` 那一处，以及同文件里其它把 `decision.ClearContext` 填进 `turn.ApprovalReply` / 提交结构的地方——`grep -n "ClearContext" pkg/tui/*.go` 逐一对照）并列加 `PlanOnly: decision.PlanOnly`
   （若中间结构 `ApprovalSubmit` 没有该字段，照 `ClearContext` 的路径加上，直到它落进 `turn.ApprovalReply`）。
4. 网关 REST：`api_extra.go` 审批 body 加 `PlanOnly bool \`json:"plan_only,omitempty"\``，传 `PlanOnly: body.PlanOnly`。

**Verify**：`go build ./...` → exit 0。

### Step 3：办公 runner 的计划工具

`pkg/tool/session_tools.go`：
1. 新增 `newExitPlanModeToolFor(st *State, stateRoot, projectKey string, rt *AgentToolRuntime) (*llm.Tool, error)`：与 `newExitPlanModeTool` 相同，另外：
   - **office 守卫**：`rt != nil && rt.ProjectType == state.ProjectTypeOffice` 且 `state.Get(stateRoot, sid)` 的 `Mode != ModePlan` → 返回 `fmt.Errorf("exit_plan_mode is only available while the user has this conversation in plan mode")`（在要审批之前判断）。
   - 批准后，`rt != nil && rt.Actions != nil` 时 `act, _ := rt.Actions.Get(ctx, id)`；`turn` 包不能被 `tool` import——所以**在本文件里**写一个私有 `actionKeptPlanOnly(act *state.Action) bool`（解析 `AnswerJSON` 的 `plan_only`；与 Step 2 的 `turn.ActionKeptPlanOnly` 同语义，注释互相指明）。
   - `planOnly == true` 时：用 `state.KeepPlanOnly(stateRoot, sid, restored)` 代替 `state.Switch`；返回 JSON
     ```json
     {"mode": "<restored>", "plan_file": "<path>", "plan_only": true,
      "message": "The user kept this plan as the deliverable and does not want it carried out now. Do not start any of its tasks or create any other file. Reply in one or two sentences: where the plan is and that you can carry it out whenever they ask."}
     ```
     不带 `on_completion`；并登记生成物：计划文件存在时 `rt.Sess.AddArtifact(ctx, state.Artifact{SessionID: ConversationSessionIDFromContext(ctx), RunID: RunIDFromContext(ctx), ToolCallID: ToolUseIDFromContext(ctx), Path: planFile, Name: filepath.Base(planFile), Title: <计划文件第一个 "# " 标题，无则文件名>, Kind/MIME/Preview: ClassifyArtifact(...), Size/MTimeNS/SHA256: 现算, Origin: state.ArtifactOriginPlan})`；
     登记失败只 `slog.Warn`，不让工具失败；成功则把 `{"artifact": {"id","name","kind","preview","size"}}` 并进返回 JSON。
   - `planOnly == false`：行为与 `newExitPlanModeTool` 逐字相同。
   让 `newExitPlanModeTool` 改为调用 `newExitPlanModeToolFor(st, stateRoot, projectKey, nil)`（`nil` rt 时 office 守卫与 plan-only 分支都不触发），保证 `NewPlanModeTools` 行为不变。
2. `registerSessionAndMemoryTools`：
   ```go
   office := rt.ProjectType == state.ProjectTypeOffice
   if !office {
   	// t3 enter_plan_mode：原样创建、注册 meta、Register
   }
   t3b, err := newExitPlanModeToolFor(st, stateRoot, projectKey, rt)
   ```
   （办公不注册 `enter_plan_mode`：用户只能显式切换进入计划模式。跑批的处理在 `149`，本计划不碰 batch 分支——batch 此时仍按 coding 注册，149 会改。）

**Verify**：`go build ./...` → exit 0；`FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/tool -run 'PlanMode' -count=1` → ok。

### Step 4：办公在 agent 模式下看不到 `exit_plan_mode`；办公提醒文字

`pkg/run/config.go`：
1. `planModeLLM` 加字段 `office bool`；新增
   ```go
   // wrapPlanModeLLMFor is wrapPlanModeLLM for a runtime of a given project
   // type. An office runtime offers exit_plan_mode only while the user has the
   // conversation in plan mode, and words its reminders for office work.
   func wrapPlanModeLLMFor(inner llm.LLM, stateRoot, projectKey, projectType string) llm.LLM
   ```
   （内部调 `wrapPlanModeLLM` 再把 `office` 置上；`inner == nil` 时返回 nil。）`pkg/run/runner.go` 约 `:1070` 改为 `wrapPlanModeLLMFor(llmClient, r.workspaceRoot(), r.ProjectKey, r.ProjectType())`。
2. `Execute`：office 的工具可见性必须 fail closed。coding 的现有早退路径一字不改；office 按下面顺序处理：
   - `stateRoot == ""`、session id 为空、或 `state.Get` 失败时，无法证明用户显式处于 plan 模式，先 `tools = withoutTool(tools, "exit_plan_mode")`，再走对应现有 inner Execute 早退路径。
   - 成功读到 `st` 后、分支之前：
     ```go
     if w.office && st.Mode != state.ModePlan {
     	tools = withoutTool(tools, "exit_plan_mode")
     }
     ```
   `withoutTool` 是新的私有函数（返回新切片，不改调用方的切片）。这样 agent 模式、缺 session、状态读取失败三条路径都不会把 exit 暴露给办公模型；plan 模式成功读取时才保留它。
3. 办公提醒：`buildPlanModeReminder` 加一个参数前的分派——新增 `buildOfficePlanModeReminder(planDir, planFile string, planExists bool, kind string, isReentry bool, isSubagent bool) string`，
   在 `Execute` 里 `if w.office { reminder = buildOfficePlanModeReminder(...) } else { reminder = buildPlanModeReminder(...) }`。文字：
   - sparse 主代理：与 coding 的 sparse 文本相同（复用 `buildPlanModeReminder(..., "sparse", ...)`，该文本不涉及代码）。
   - sparse 子代理（逐字格式，`%s` = planDir；不要继承 coding 的 `AskUserQuestion` 名称）：
     `Plan mode active (subagent). Write/edit plan files only inside %s - all other writes blocked. Use user_interaction to clarify the approach.`
   - 子代理 full（**逐字使用，不能对 coding 文本做 Replace 后继承代码工具名**；`%s` = planDir）：
     ```
     PLAN MODE ACTIVE (subagent) - read carefully.

     You are a subagent helping with office planning. You MUST NOT create or change any file except plan files inside this dedicated plan directory:

       %s

     ## Tools in subagent plan mode

     - Reading documents and data, searching the folder, web research and read-only commands: free use.
     - user_interaction: free use to clarify the approach with the parent agent or the user.
     - write_file, edit_file: only inside the plan directory above.
     - Anything that creates or changes other files: blocked.
     - exit_plan_mode: NOT available; only the parent agent can exit plan mode.
     - subagent_run, subagent_fanout: NOT available within a subagent.

     ## Important

     - Read the sources that matter and contribute findings, source references, open decisions and a proposed deliverable structure.
     - Do not produce the deliverable itself. When done, summarize your findings; the parent agent will decide how to proceed.
     ```
   - 主代理 full（逐字使用；`%s` 依次为 planDir、exists、reentrySection——这两个变量与 coding 版同样计算）：
     ```
     PLAN MODE ACTIVE - read carefully.

     The user switched this conversation to plan mode: plan the work before doing it. You MUST NOT create or change any file except plan files inside this dedicated plan directory:

       %s
     %s
     %sAuthor plan files with write_file (create or replace) or edit_file (targeted change). Every other write is blocked.

     ## Plan files keep their history

     - Give each plan its own file with a descriptive name based on the task (for example "q3-board-report.md" or "prospectus-review-checklist.md"). Do not use the generic name "plan.md".
     - To refine the current plan, edit it in place; for a substantially new plan, create a new file. The most recently modified .md file is the one shown for approval.

     ## How to plan office work

     1. Understand: read the files in the folder and the attachments that matter, and note what each source contributes.
     2. Design each deliverable: its format; its structure (sections, sheets or slides); what goes in each part and from which source; the calculations or data processing it needs; the template or style to follow.
     3. Surface decisions: list your assumptions and the questions only the user can answer. When an answer would change the plan, ask with user_interaction, all questions in one go.
     4. Write the plan file with these sections: Goal, Deliverables, Sources, Outline of each deliverable, Steps, Checks before hand-over, Open questions.
     5. Call exit_plan_mode when the plan is complete. The user can have it carried out, ask for changes, keep the plan itself as the deliverable, or ask another model to review it.

     ## Tools in plan mode

     - Reading files, searching, web research and read-only commands: free use.
     - user_interaction: free use to clarify the plan.
     - write_file, edit_file: only inside the plan directory above.
     - Anything that creates or changes other files: blocked until the plan is approved.

     ## Important

     - Do not produce any deliverable in plan mode; the plan describes it.
     - Do not use user_interaction to ask "is this plan okay?" - that is what exit_plan_mode does.
     ```
     re-entry 段与 `exists` 文本复用 coding 版（它们不涉及代码），但 exists 示例名改为 `'q3-board-report.md'`：在 office 函数里自己拼 exists 字符串（照 coding 的两个分支写，只换示例名）。
4. `executeImplementationPhase`：`if !st.HasExitedPlan || st.PlanKeptOnly { return w.inner.Execute(ctx, msgs, tools) }`（PlanKeptOnly 对 coding 永远是 false，行为不变）。
   办公措辞：`w.office` 时用新的 `buildOfficePlanImplementationReminder(planFile, kind)`：sparse 为
   `"%s: before you report the work finished, update the plan file %s - mark what was done and append a '%s' section listing the files delivered."`（三个参数同 coding）；
   full 为（以 `planImplementationReminderMarker` 开头，计数靠它）：
   ```
   %s - read carefully.

   You are carrying out an approved plan. The plan file is:

     %s

   It is writable in this mode.

   ## Closing the plan loop

   When the plan's tasks are done - or when you stop working on the plan for any reason - update the plan file BEFORE your final answer:

   1. Mark each task as done, partly done or not done.
   2. Append a final section with exactly this heading:

      %s

   3. Under it record the files delivered (names and locations), any deviation from the plan and why, anything planned but not done and why, and how you checked the deliverables.

   Use edit_file to append the section; keep the plan's original text readable.
   ```
5. **提示词 golden**：把主代理 full、子代理 full、子代理 sparse、实施 full、实施 sparse 分别提取为编译期常量
   `officeMainPlanFullFormat`、`officeSubagentPlanFull`、`officeSubagentPlanSparseFormat`、`officeImplementationFullFormat`、`officeImplementationSparseFormat`。
   上述代码块就是常量全文（含 `%s`、换行与连字符，不得重排）。逐字写入 `pkg/run/testdata/office_plan_prompts.golden.json`，key 与常量名相同。
   `TestOfficePlanPromptsGolden` 比较常量与 JSON；再以 `/plans`、planExists=true/false、isReentry=true/false 渲染，覆盖主代理/子代理、full/sparse 与实施提醒，防止 `%s` 顺序错误。
   `plan_only` 的工具结果 message 不跨包复用该 golden：在 `pkg/tool/session_tools_test.go` 用 Step 3 的完整英文句子逐字断言。

**Verify**：`go build ./...` → exit 0；`FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/run -count=1` → 无新增失败。

### Step 5：卡头与正文

1. **先补展示事实，不读不存在的 `meta.Output`**（已核实 `tool.ToolMeta` / `event.ToolCallMeta` 目前只有 Input，没有 Output）：
   - `pkg/tool/format.go` 把 `formatExitPlanModeStep` 当前对 `evt.Output["output"]` 的 JSON 解码提取成私有 `exitPlanModeResult(evt)`，同时供正文与 `BuildToolMeta` 使用；结构含 `PlanOnly`、`Artifact`。
   - `BuildToolMeta` 在 exit_plan_mode 完成且解析到 `plan_only:true` 时设 `meta.PlanOnly = true`。`pkg/tool/notification.go` 的 `ToolMeta`、`pkg/event/tool_events.go` 的 `ToolCallMeta` 均加 `PlanOnly bool \`json:"plan_only,omitempty"\``；
     `RunEventFromStep` 逐字段投影时复制它，`pkg/gateway/wsevents.go` 的 `toolCallMetaFromWS` 用 `boolField(metaMap,"plan_only")` 读回。
   - `pkg/tui/notify.go`、`pkg/tui/chat_session.go` 中手工复制 ToolMeta 的现有位置，在 DenyFeedback/ResultLines 旁并列复制 `PlanOnly`；不得只改 live 而漏 replay。
   - `pkg/tool/header_parts.go` 的 `exit_plan_mode` 分支在非 denied/failed 且 `meta.PlanOnly` 时 `action = "Kept the plan"`。
   `HeaderActions()` 在 `"Exited plan mode", "Failed to exit plan mode", "Kept planning",` 后加 `"Kept the plan",`；跑 golden 更新。
2. `ai-elements-vue/packages/elements/src/tool/headerWords.ts` 加 `'Kept the plan': '已保留计划'`。
3. `pkg/tool/format.go` `formatExitPlanModeStep` 改用上述 `exitPlanModeResult`：`PlanOnly` 时正文为 `"Kept the plan as the deliverable: <plan_file>"`（不显示给模型的那句 message）。其它情况不变；畸形 JSON 仍走现有 generic renderer，不能造出 PlanOnly。

**Verify**：`CGO_ENABLED=1 go test -tags fts5 ./pkg/tool -count=1` → ok；`cd frontend && pnpm vitest run src/locales/toolHeader.test.ts` → passed。

### Step 6：终端四个选项

1. `pkg/tui/channels.go`：`ApprovalSink` 加 `projectTypeOf func(sessionID string) string` 与链式 `WithProjectTypeOf(fn func(string) string) *ApprovalSink`；
   `PromptToolApproval` 开头 `if s.projectTypeOf != nil && strings.TrimSpace(req.ProjectType) == "" { req.ProjectType = s.projectTypeOf(req.SessionID) }`。
2. `pkg/tui/chat_session.go` 加
   ```go
   // SessionProjectType is the type a conversation was born with, coding when
   // it cannot be read.
   func (s *ChatSession) SessionProjectType(sessionID string) string
   ```
   （`s.projectStoreFor().SessionProjectType(context.Background(), sessionID)`，出错 / nil → `state.ProjectTypeCoding`。）
   `pkg/tui/run.go` 约 `:196` 的链上加 `.WithProjectTypeOf(opts.Session.SessionProjectType)`（`opts.Session` 为 nil 时不要调用——照该行附近已有的 nil 处理方式）。
3. `pkg/tui/overlays.go`：`overlayChoice` 加 `planOnly bool`；`buildExitPlanChoices` 开头：
   ```go
   if req.ProjectType == state.ProjectTypeOffice {
   	choices := []overlayChoice{
   		{label: "Yes, carry out the plan", allowOnce: true},
   		{label: "No, revise the plan", denyOnce: true, isPlanDeny: true},
   		{label: "Keep the plan only, don't carry it out", allowOnce: true, planOnly: true},
   	}
   	if len(req.PlanReviewModels) > 0 {
   		choices = append(choices, overlayChoice{label: "Ask another model to review this plan", isPlanReview: true})
   	}
   	return choices
   }
   ```
   （`pkg/tui` 若未 import `pkg/state`，`grep` 确认；`pkg/tui` 包已 import `pkg/state`（`chat_session.go` 用了 `state.NewProjectStore`），在 `overlays.go` 加 import 行不增加包级 fan-out。）
   约 `:1373` 生成决定处改为 `turn.ToolApprovalDecision{Approved: true, ClearContext: choice.clearContext, PlanOnly: choice.planOnly}`。
   "No, revise the plan" 复用 `isPlanDeny` 的全部现有行为（必须写说明、Esc 等）——不要新写一套输入逻辑。
4. coding 的 `buildExitPlanChoices` 其余代码**一字不改**。

**Verify**：`FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -count=1` → 无新增失败。

### Step 7：测试（见 Test plan）

### Step 8：终端真机核对（fake provider 即可，不需要真模型）

按 `.claude/skills/run-forebrain/SKILL.md` 起 TUI（隔离 home、独立目录）。准备：在隔离 home 的状态库里把启动目录对应的项目改成 office
（先启动一次让 TUI 登记项目 → 退出 → `sqlite3 <home>/state.sqlite "UPDATE fb_projects SET project_type='office'"` → 再启动，新会话即 office）。
用 fake provider 脚本让模型：在 agent 模式下请求的工具表里**没有** `enter_plan_mode`/`exit_plan_mode`（读 fake provider 记录的请求 JSON 的 tools 名单核对）；
用户按模式切换键进入 plan 模式后，下一次请求的工具表里**有** `exit_plan_mode`、没有 `enter_plan_mode`；脚本让模型写计划文件并调用 `exit_plan_mode` →
截屏核对审批面板正好四行（无审阅模型时三行）且文字与 Step 6 一致；选第三行 → 卡头 `Kept the plan`、正文 `Kept the plan as the deliverable: …`、`fb_artifacts` 多一行 `origin='plan'`、
模式回到 agent、下一轮请求里没有 "Plan follow-through required"。若 fake provider 不支持按轮次脚本化工具调用，记录 NOT RUN 并说明，由评审方补跑。

## Test plan

1. `pkg/state`：`TestKeepPlanOnlyLeavesPlanModeAndMarksIt`（plan → KeepPlanOnly → Mode agent、HasExitedPlan、PlanKeptOnly）；`TestEnteringPlanModeClearsPlanKeptOnly`。
2. `pkg/turn/approval_test.go`：`TestDecidePlanOnlyOnlyForApprovedExitPlan`（非 exit_plan_mode / deny / 与 clear_context 同时 → 错误）；`TestPlanOnlyIsRecordedAndIdempotent`（AnswerJSON 含 `"plan_only":true`；同 reply 重放 Idempotent；不同 reply → ErrApprovalConflict）；`TestActionKeptPlanOnly`。
3. `pkg/tool/session_tools_test.go`：
   - `TestOfficeRuntimeRegistersNoEnterPlanMode`：`RegisterDefaultTools` 用 `ProjectType: office` → 工具里无 `enter_plan_mode`、有 `exit_plan_mode`；coding → 两个都有。
   - `TestOfficeExitPlanModeRefusedOutsidePlanMode`。
   - `TestExitPlanModePlanOnlyKeepsThePlanAndRecordsAnArtifact`：批准动作的 `AnswerJSON` 带 `plan_only` → 返回 JSON 有 `plan_only:true` 且**无** `on_completion`；模式文件 `plan_kept_only:true`；`fb_artifacts` 一行 `origin='plan'`、`kind='markdown'`。
   - `TestExitPlanModeWithoutPlanOnlyIsUnchanged`：返回 JSON 与本计划前逐字段一致（含 `on_completion`）。
4. `pkg/run/config_test.go` + `pkg/run/testdata/office_plan_prompts.golden.json`（照现有 `wrapPlanModeLLM` 测试的假 LLM 写法）：
   - `TestOfficeAgentModeRequestHasNoExitPlanMode` / `TestOfficePlanModeRequestHasExitPlanMode`（记录内层收到的 tools 名单）；另加 `TestOfficeModeReadFailureHidesExitPlanMode` 与空 session id 变体。
   - `TestOfficePlanPromptsGolden`：五个提示词常量逐字匹配，两个 exists/reentry 维度与 full/sparse 的渲染场景全部覆盖；`TestCodingPlanReminderUnchanged`（coding full 文本含 `Phase 1 - Initial Understanding: explore the codebase`）。
   - `TestOfficeSubagentPlanReminderSaysNothingAboutCode`：不得含 `codebase`、`code_search`、`references`、`definitions`、`AskUserQuestion`，必须含 `user_interaction` 与 `NOT available`。
   - `TestPlanKeptOnlySuppressesImplementationReminder`。
5. `pkg/gateway/api_extra_test.go`：`TestApproveExitPlanWithPlanOnly`（照现有 clear_context 的审批测试，grep `clear_context` 找；body `{"plan_only":true}` → 动作 AnswerJSON 含 `plan_only`；对非 exit 动作 → 4xx）。
6. `pkg/tool` + `pkg/gateway/wsevents_test.go`：`TestPlanOnlyMetadataSurvivesLiveAndReplay`——同一 StepEvent 经 `BuildToolMeta`、`RunEventFromStep`、WS map/`toolCallMetaFromWS` 与 TUI 的 meta 复制后 `PlanOnly` 都为 true，卡头均为 `Kept the plan`；普通退出与畸形结果均为 false。
7. `pkg/tui/overlays_test.go`：`TestOfficeExitPlanChoices`（office 三行 / 有审阅模型四行，标签逐字、第二行 `isPlanDeny`、第三行 `planOnly`）；`TestCodingExitPlanChoicesUnchanged`（原四行逐字）。

## Done criteria

- [ ] `go build ./...` exit 0；`FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/tool ./pkg/run ./pkg/state ./pkg/turn ./pkg/gateway ./pkg/tui ./pkg/architecture -count=1` 无新增失败；Test plan 的新测试都存在且通过
- [ ] `cd frontend && pnpm vitest run src/locales/toolHeader.test.ts` passed；golden 只多 `Kept the plan`
- [ ] `grep -n "wrapPlanModeLLM(" pkg/run/*_test.go | wc -l` 仍是 29（签名未动）
- [ ] Step 8 终端核对 PASS 或 NOT RUN（写明原因）
- [ ] `plans/README.md` 本计划状态行已更新

## STOP conditions

1. 前置不满足或"现状"对不上。
2. 任何 coding 的现有测试因本计划变红（说明 coding 行为被动了）。
3. 审批结果在网关 / 终端之外还有第三条把 `ClearContext` 写进 `ApprovalReply` 的路径，而你没法确定 `PlanOnly` 该不该跟着走——列出路径停下。
4. `plan_only` 在 BuildToolMeta → canonical event → WS/TUI replay 的任一现有投影路径无法保留——列出断点停下，不要在界面从正文猜布尔值，也不要给卡头另造数据源。
5. 用户显式切换进 plan 模式的入口（TUI 模式键、`/plan`、web `switch_mode`）有任何一处经过 `enter_plan_mode` 工具而不是直接 `state.Switch`——停下报告（办公不注册该工具会让它失效）。

## Maintenance notes

- `plan_only` 只对办公有入口（终端与网页的选项组），引擎不校验会话类型；若以后 coding 也要"只要计划"，只需在 coding 选项组加一行。
- `PlanKeptOnly` 存在模式文件里（`state/modes/<sid>.json`），不是状态库——owner 2026-10-10 已裁定"文件型会话数据入库以后再说"。
- 办公的 `exit_plan_mode` 是否出现在工具表取决于**每次请求时**的会话模式：用户在一轮中途切换模式，下一次请求即生效（提示缓存会因工具表变化失效一次，可接受）。
