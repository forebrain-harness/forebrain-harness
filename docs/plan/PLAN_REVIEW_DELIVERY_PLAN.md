# Plan: 评审意见回传 primary agent 并驱动计划修订（PLAN_REVIEW_DELIVERY）

> **Executor instructions**: Follow this plan step by step. Run every verification
> command and confirm the expected result before moving on. On any STOP condition,
> stop and report. After implementation, copy this plan to
> `docs/plan/PLAN_REVIEW_DELIVERY_PLAN.md` (repo convention:
> `docs/plan/<UPPER_SNAKE>_PLAN.md`) and append the acceptance record. Do NOT
> commit — the owner reviews and commits manually.

## Status

- **Priority**: P1
- **Effort**: L
- **Risk**: MED（审批流共享层行为变更 + TUI/web 两端文案与回放）
- **Depends on**: none
- **Category**: bug（功能特性未实现：评审意见从未到达 primary agent）
- **Planned at**: working tree on local `main` @ `1fd93a1`（工作区干净；drift check = 对照本文 file:line 与现码）

## Why this matters

owner 报告：exit_plan_mode 审批时选择其他模型 review plan，plan-reviewer 完成并给出评审意见后，primary agent 没有根据意见修改计划。经现场取证确认：**"评审意见 → primary agent 修订计划"这条链路从未实现**。评审全文只写入 `plan_reviewed` 事件（UI 展示用）；进入模型的唯一通道是用户手动选"继续规划/拒绝"时 `ComposeDenyFeedback` 拼进拒绝理由（`docs/plan/SUBAGENT_CONVERSATION/016-shared-plan-review-and-web-exit-plan.md` §缓存影响明确旧契约："plan_reviewed 是界面事件，不进入任何模型请求"）。用户看完评审直接批准，评审意见对 primary 完全不可见——功能特性不生效。

owner 已拍板两个行为决策（2026-10-08，user_interaction）：
1. **评审完成即自动回传**：评审 `outcome=done` 后，引擎自动把当前 exit_plan_mode 审批按"评审回传"语义关闭（内部 deny），评审全文注入 primary 的恢复输入；primary 修订计划文件后重新 `exit_plan_mode`，产生新审批卡。用户审批的始终是吸收过评审的计划。副作用（owner 已知）：回传后不能再"批准原计划"，只能批准修订版。
2. **运行中批准 = 批准原计划 + 自动中止在途评审**：批准路径 best-effort 取消 in-flight reviewer；取消竞态输给评审完成时，回传的 deny CAS 撞 `ErrNotPending` 自动跳过（评审只留事件）。

## Root cause（证据链）

### 现场证据（真实状态库 `/Users/doudou/.forebrain/state/forebrain.state.sqlite`，会话 `cli-5130bd73-3778-4d2a-a812-041e1353fba9`，2026-10-08）

| sequence | event | 要点 |
|---|---|---|
| 4063907 | `approval_requested` | exit_plan_mode，tool_step_id=call_00_ET_tzz… |
| 4063908 | `approval_resolved` | decision=review_requested（用户请 zhipuai/glm-5.3-flash 评审） |
| 4063909 | `plan_review_started` | review_id=plan-review:1e8c31be… |
| 4063910 | `subagent_spawned` | agent_type=plan-reviewer，execution_id=1e153585…（worker run） |
| 4065652 | `subagent_ended` | reviewer 结束 |
| 4065654 | `plan_reviewed` | **outcome=done，text=完整评审正文**（"# 计划复核：启动卡片配色对齐 … Verdict: approve with changes …"），duration_ms=92200 |
| 4065656 | `approval_resolved` | decision=approved（用户直接批准） |
| 4065658+ | `tool_call_started exit_plan_mode` → read_file/shell/edit_file/plan_updated | primary 直接开始实施 |

主会话 `fb_messages`（id 98684–99106）中，4065654 的评审文本**没有出现在任何后续模型输入**（user/tool/assistant 行均无）。reviewer 的最终 assistant 消息被完整捕获（`assistant_delta` 全部带 `agent_id=subagent-c3a3316c…`，汇总进 PlanReviewedPayload.Text）——不是消息丢失，是没有任何代码把它发给 primary。

### 代码链（file:line，全部为 1fd93a1 现码）

1. **reviewer 返回链完好**：`process.PlanReviewer.Review`（`pkg/process/run_executor.go:333-361`）→ `Runner.RunPlanReviewSubagent`（`pkg/run/subagent.go:3288-3323`）→ `runPlanReviewAcrossApprovals`（`:3362-3374`）→ `executeSubagent`（`:520`）→ `RunSubagentExec` 返回 `res.TextContent()`（`pkg/process/one_shot.go:20-28`）。最终 assistant 文本 = `result.Text`。
2. **`turn.RunPlanReview`（`pkg/turn/approval.go:1384-1446`）**：`result.Text` 只经 `finish()` 发布为 `plan_reviewed` 事件（`PlanReviewedPayload.Text`），函数返回 `nil`，调用方不消费文本。
3. **评审进模型的唯一通道是拒绝恢复**：`BuildApprovalResumeWithPlanReviews`（`pkg/turn/approval.go:129-142`）仅在 `out.Denied && toolName==exit_plan_mode` 时 `out.Reason = ComposeDenyFeedback(results, out.Feedback)`。TUI `resumeAgentContext`（`pkg/tui/chat_turn.go:746-755`）与 gateway `resumeGatewayRun`（`pkg/gateway/api_extra.go:3088-3118`）都只在 denied 分支组装 `ToolApprovalResumeState.DenyReason`。
4. **denial 注入机制**：`toolOrchestrationLLM.consumeResumeSnapshot` + `buildDenialMessage`（`pkg/run/orchestration_llm.go:107-137, 432-486`）把 `DenyReason` 写成 denial tool result 进入 parked run 的 session——这条链完整、被 `TestToolOrchestrationResumeDeniedWithFeedback`/`TestDeniedToolResultCarriesItsDisplay`（`pkg/run/orchestration_llm_test.go:4326+, 4423`）覆盖。**修复就是复用它**。
5. **两个 surface 的评审启动点**：TUI `completeSurfacePlanReviewRequest` → `runPlanReview`（`pkg/tui/chat_surface.go:817-901`，同步阻塞调 `turn.RunPlanReview`）；gateway `handleActionPlanReview`（`pkg/gateway/api_extra.go:2765-2862`，`go func(){ _ = turn.RunPlanReview(...) }()`）。
6. **既有测试只覆盖拒绝路径**：`TestPlanReviewLeavesTheApprovalPendingAndReachesThePlanner` / `TestPlanReviewReachesThePlannerWhenTheUserTypesNothing`（`pkg/tui/chat_session_test.go:12448+, 12532+`）断言的都是"用户再拒绝时评审到 planner"；"评审 done 后自动送达 primary"零覆盖。
7. **审批状态机**：`fb_actions.status` STRICT 枚举 `pending/answered/approved/denied/cancelled/expired/error`（`pkg/state/action_service.go:409`）；`ActionService.Deny` 是 pending→denied 的 CAS（`:270-292`）。不加新状态（避免 schema 迁移），内部 deny 复用 `denied` + 专用 reason 标记。
8. **在途评审探测与取消**：`turn.ActivePlanReview`（`pkg/turn/approval.go:1193+`，从事件推导 in-flight review 的 ReviewID/AgentID）；TUI 已有 `planReviewCancel`（`pkg/tui/chat_session.go:130-134, 976`，Esc 取消评审用）；gateway 取消 reviewer 走 subagent cancel（web 的 `stopReview` 即 `forebrainApi.subagentCancel(agentId)`，`frontend/src/components/chat/PendingActionsPanel.vue:543-547`）。

**结论**：根因 = 评审结果的消费端只实现了「UI 展示」与「用户拒绝时拼 DenyReason」两处，缺第三处「评审完成自动回传 planner 并驱动重新审批」。修复归一到既有 denied-resume 语义，不新造传输机制。

## Fix design

### 1. 引擎（pkg/turn/approval.go，不新建文件）

- 新增导出常量：
  ```go
  // PlanReviewDeliveredReason marks the internal denial that hands a completed
  // review back to the planning model. It is never the user's words: the UI
  // reads it to say the review was delivered, and the resume composer treats
  // it as "no user feedback" so the model gets the review alone.
  const PlanReviewDeliveredReason = "plan-review:delivered"
  ```
- 新增导出函数 `ComposeReviewDeliveryGuidance(results []PlanReviewResult) string`：
  - 指令头（编译期常量，provider-neutral，供缓存稳定）：
    `"The plan review you asked for has returned. The user has not decided yet: this approval was closed only to hand the review to you. Weigh each review, update the plan file, then call exit_plan_mode again to submit the revised plan for approval."`
  - 其后接 `ComposeDenyFeedback(results, "")` 的产出（评审正文段，无 "user feedback" 段）。
- `BuildApprovalResumeWithPlanReviews`（`:129-142`）加白名单：`out.Feedback == PlanReviewDeliveredReason` 时先置 `out.Feedback = ""` 再组合，且该场景 `out.Reason = ComposeReviewDeliveryGuidance(results)`（而非 ComposeDenyFeedback）。这样 TUI/gateway 两条既有 denied-resume 路径自动获得正确模型输入，无需各自拼接。
- 新增导出函数 `DeliverPlanReview(ctx context.Context, actions ApprovalStore, actionID string) (*state.Action, error)`：
  - 读 action；非 pending 或 kind≠exit_plan_mode → 返回 `(nil, state.ErrNotPending)`（调用方按 no-op 处理——批准竞态的自然收敛点）。
  - `actions.Deny(ctx, actionID, PlanReviewDeliveredReason)`，返回 deny 后的 action。
  - 这是唯一写点；surface 不各自 CAS。

### 2. TUI（pkg/tui/chat_surface.go + chat_turn.go + channels.go）

- `runPlanReview`（`chat_surface.go:848-901`）：`_ = turn.RunPlanReview(...)` 改为捕获 err：
  - `err != nil`（评审失败/无计划）：维持现状（卡片报失败，审批不动）。
  - `err == nil`（done）：调 `s.deliverPlanReview(ctx, actionID)`：
    1. `act, derr := turn.DeliverPlanReview(ctx, s.actionSvc(), actionID)`；`state.ErrNotPending` → 静默返回（用户已批准/已另作决定）。
    2. 发布 `approval-resolved:<id>:denied` 事件（复用 `approvalResolvedEventID`，payload Decision="denied"、Reason=act.Error）——与 `completeSurfaceApproval` 的发布同构。
    3. UI 提示一行（不打印用户决策确认行，这不是用户决策）：`notifyUI` 一条 `FrameStatus`/NewMessage："Plan review delivered — the planner is revising the plan."（新文案，中英由 TUI 现有风格定，TUI 历来英文）。
    4. `s.retryResumeDispatch(actionID, "deny", s.resumeAfterDenial)`——`resumeAfterDenial` → `runResumeDeniedTurn` → `resumeAgentContext` → `BuildApprovalResumeWithPlanReviews`（白名单生效：DenyReason=指令头+评审，Feedback=""）。
- `notifyToolApprovalDenied`（`chat_turn.go:1023-1082`）：`feedback == turn.PlanReviewDeliveredReason` 时 denial 卡 `content` 改为 `"Plan review delivered — the planner is revising the plan."`（替代 `DeniedToolDisplayBody` 的空 feedback 句）。
- 批准中止在途评审：`completeSurfaceApproval` 的 `result.Approved` 分支（`chat_surface.go:530-532` 附近）加 best-effort：若 `act.Kind==exit_plan_mode` 且 `s.planReviewCancel != nil` → 调用 cancel（评审 goroutine 的 `RunPlanReview` 会以 stopped 收尾）。
- `approvalConfirmationText` 不改：自动回传不经用户决策 sink，不会误打 "You did not approve…"。

### 3. Gateway（pkg/gateway/api_extra.go）

- `handleActionPlanReview` 的 goroutine（`:2860`）：`err := turn.RunPlanReview(detached, review)`；`err == nil` 时：
  1. `act, derr := turn.DeliverPlanReview(detached, s.Actions, id)`；`state.ErrNotPending` → return。
  2. 发布 `approval-resolved` 事件（同 `resolveGatewayApproval` 的发布块，`:2970-2983`）。
  3. `go s.resumeGatewayRun(id, false)`——其 `BuildApprovalResumeWithPlanReviews`（`:3092`）白名单生效。
- 批准中止在途评审：`resolveGatewayApproval` 的 `result.Approved && act.Kind==exit_plan_mode` 分支：`turn.ActivePlanReview(ctx, s.RunRT, sid, id)` 命中 AgentID → 调服务端 subagent cancel（与 `stopReview` 同一内部函数；若 cancel 失败仅记日志）。取消竞态输给评审完成 → `DeliverPlanReview` CAS 撞 `ErrNotPending` → no-op。

### 4. Web 前端（frontend/src/components/chat/PendingActionsPanel.vue + locales + useChatStream）

- 回放/实时对 `approval_resolved(decision=denied, reason="plan-review:delivered")` 的显示：denied 行文案映射为新键（中："评审已送达，规划模型正在修订计划"；英："Plan review delivered — the planner is revising the plan."），不显示原始标记串。改动点：时间线 denial 块的 reason 渲染处（`useChatStream` 的 approval_resolved 处理 / PendingActionsPanel 若仍持卡）。
- 其余不动（plan_reviewed 卡、评审进行中状态、stop 按钮照旧）。

### 行为变化矩阵

| 场景 | 旧 | 新（标注刻意修订） |
|---|---|---|
| 评审 done | 审批卡重新提示（带评审 notes）；意见不进模型（除非再手动拒绝） | **自动内部 deny（标记 reason）→ 评审全文+指令头注入 primary 恢复 → planner 修订 → 新 exit_plan_mode 审批**【本修复目标】 |
| 评审 failed/stopped/timeout | 审批保持 pending，卡片报失败 | 不变 |
| 评审运行中用户批准 | 评审照跑完只留事件 | **批准原计划 + best-effort 取消在途 reviewer**；取消竞态失败→回传 CAS `ErrNotPending` no-op【owner 拍板】 |
| 用户手动"继续规划"（无评审） | DenyReason=用户话语 | 不变 |
| 用户手动"继续规划"（评审 done 后的竞态窗内） | 评审拼入 DenyReason | 回传已先 deny → 用户决策走 Idempotent/ErrNotPending 收敛，最终模型输入一致 |
| replay：denied 卡 reason=标记 | （新状态） | 显示"评审已送达"文案，不显示标记串 |
| 主会话 prompt cache | 拒绝路径 denial 注入不变前缀 | 回传与手动拒绝同构（denial tool result 注入 parked session），前缀不分叉 |

## Scope

**In scope**
- `pkg/turn/approval.go`：常量 + `ComposeReviewDeliveryGuidance` + `DeliverPlanReview` + `BuildApprovalResumeWithPlanReviews` 白名单。
- `pkg/tui/chat_surface.go`、`pkg/tui/chat_turn.go`：回传触发、denial 卡文案、批准中止。
- `pkg/gateway/api_extra.go`：goroutine 回传、批准中止。
- `frontend/src/components/chat/PendingActionsPanel.vue`、`frontend/src/composables/useChatStream.ts`、`frontend/src/locales/index.ts`：标记 reason 的显示文案（中英同键）。
- 测试：`pkg/turn/approval_test.go`、`pkg/tui/chat_session_test.go`、`pkg/gateway/api_extra_test.go`、frontend 组件测试。

**Out of scope（明确不碰）**
- `buildDenialMessage` 通用文案（`pkg/run/orchestration_llm.go:479`）——影响所有 denial golden；指令头已放在 DenyReason 内，base 句保留。
- `fb_actions` schema / 新 action 状态——内部 deny 复用 `denied`。
- plan-reviewer 执行器与 subagent 通道（`pkg/run/subagent.go`、`pkg/process/run_executor.go`）——返回链已正确。
- 评审失败/超时路径、多评审聚合上限（`planReviewMaxReviews`）。
- `pkg/gateway/dist`（由构建流程再生成）。

## Steps

1. **失败测试先行（pkg/turn）**：
   - `TestDeliverPlanReviewDeniesPendingExitPlan`：pending exit_plan action → `DeliverPlanReview` → status=denied、Error=`PlanReviewDeliveredReason`；非 pending → `ErrNotPending`；非 exit_plan kind → `ErrNotPending`。
   - `TestReviewDeliveryGuidanceCarriesReviewWithoutUserFeedback`：`ComposeReviewDeliveryGuidance` 含指令头与评审文本、不含 "outranks the reviews" 段。
   - `TestBuildApprovalResumeTreatsDeliveredReasonAsNoUserWords`：action{denied, Error=标记} → `Reason` 含指令头+评审、`Feedback==""`。
   - 验证：三条编译失败/FAIL。
2. **实现 pkg/turn**（Fix design §1）。验证：步骤 1 转绿；`CGO_ENABLED=1 go test -tags fts5 ./pkg/turn -count=1` → ok。
3. **失败测试先行（pkg/tui）**：
   - 新 `TestPlanReviewAutoDeliversToPlannerAfterDone`（以 `newPlanReviewSession` 为模板）：请求评审（stubReviewer done）→ 断言 action 变 denied(Error=标记)；`resumeAgentContext` 的 `ToolApprovalResumeState.DenyReason` 含评审文本与指令头、不含标记串；`Feedback==""`。
   - 更新 `TestPlanReviewLeavesTheApprovalPendingAndReachesThePlanner`（`:12463`）：评审 done 后 `act.Status` 断言由 pending 改为 denied（行为契约修订：评审即回传）；其后"用户拒绝"段改为验证 Idempotent 收敛或拆为新用例，注明理由。
   - `TestPlanReviewReachesThePlannerWhenTheUserTypesNothing`：搭建改为先 deny 再构造（语义仍成立：无用户话语时评审仍到 planner）。
   - 验证：新用例 FAIL。
4. **实现 TUI**（§2）。验证：步骤 3 转绿；`CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -count=1` → ok（若既有浮层/replay 测试因"评审 done 后不再重提示原审批"失败，逐条核对是否本行为变更所致并更新断言+注明；超出此范围的失败 → STOP）。
5. **失败测试先行（pkg/gateway）**：扩展 `TestActionPlanReviewValidatesBeforeStarting`（`api_extra_test.go:3511+`）或新增 `TestActionPlanReviewDeliversOnDone`：评审 done → action denied(标记) + `approval_resolved` 事件 + resume 提交的恢复输入含评审（复用该文件 detached 基建；无 subagent executor 的 server 评审 outcome=failed 的既有断言保持）。新增批准中止用例：approved + `ActivePlanReview` 命中 → cancel 被调用。验证：FAIL。
6. **实现 gateway**（§3）。验证：转绿；`CGO_ENABLED=1 go test -tags fts5 ./pkg/gateway -count=1` → ok。
7. **frontend**：标记 reason 文案映射 + 组件测试（denied 行显示新文案、不显示标记串）；`cd frontend && corepack pnpm test` + `corepack pnpm exec vue-tsc --noEmit -p tsconfig.json` → 0。
8. **全量回归**：`gofmt -l pkg cmd` 空；`go vet ./...` 0；`CGO_ENABLED=1 go test -tags fts5 ./... -count=1` 全 ok；`scripts/package-graph.sh` 无 diff。
9. **拷贝计划**到 `docs/plan/PLAN_REVIEW_DELIVERY_PLAN.md` 并回填验收记录。
10. **tmux 真机验收**（构建必须 `-tags fts5`，隔离 `FOREBRAIN_HOME` + 临时项目目录，智谱双模型）：
    - 场景 A（主路径）：plan mode → 写计划 → exit_plan_mode → 审批浮层选"请其他模型评审" → 评审卡运行 → done 后断言：原审批关闭、出现"评审已送达"行、`fb_messages` 中出现 denial tool result 且内容含评审正文与指令头、planner 修订计划文件、**新的 exit_plan_mode 审批卡**出现、批准后实施。逐屏 capture 取证。
    - 场景 B（批准中止）：评审运行中批准（TUI 若浮层不可达则用 web 卡片路径）→ reviewer run 取消（`plan_reviewed outcome=stopped`）→ 实施照常。
    - 场景 C（回归）：无评审的手动"继续规划"拒绝 → DenyReason 仍为用户话语（不含指令头）。
    - 场景 D（中断自救）：回传恢复进行中 Ctrl+C → EXIT_CODE 干净 → `/resume` 后审批状态一致（denied 卡+新审批或可恢复等待）。

## Done criteria（machine-checkable）

- [x] `rg -n "plan-review:delivered" pkg frontend/src` 仅命中 `PlanReviewDeliveredReason` 定义、白名单判定与文案映射（无裸串扩散）。（实测：pkg 仅 approval.go 常量定义一处；frontend/src 仅测试 fixture 与反断言；生产代码零裸串——文案走 `Confirmation` 通道，见验收记录偏差 1。）
- [x] 步骤 1/3/5 的新测试存在且全绿；`./pkg/turn ./pkg/tui ./pkg/gateway` 包级回归 ok。（另含 pkg/run 的新 display 测试，见偏差 4。）
- [x] 真机场景 A–D 取证入验收记录；`fb_messages` 证据含评审正文注入。
- [x] `git status` 改动范围与 Scope 一致，未 commit。（工作区另有 owner 既有未提交改动：pkg/gateway/http_server*.go、serve_run.go、pkg/tui/render.go，非本计划产物，未触碰。）

## Acceptance record（2026-10-08）

### 场景取证（真实智谱双模型：主 glm-5.3，评审 glm-5.3-flash；隔离 FOREBRAIN_HOME=/tmp/forebrain-prd）

**场景 A（主路径，TUI）**：plan mode 提交任务 → planner 写计划 → exit_plan_mode 浮层 → 选 4「Ask another model to review this plan」→ 选 glm-5.3-flash → 评审卡运行 1m18s → done 后自动回传：
- 屏面：`◆ Kept planning └ Plan review delivered — the planner is revising the plan.` + `● system` 同句提示；原审批不再重提示。
- planner 修订后重新 exit_plan_mode，新审批卡带「Review feedback (glm-5.3-flash, verdict: approve) … Adopted: go test ./... / Declined: ××」——评审意见被 primary 吸收（采纳 1 条、拒绝 2 条并给理由）。
- 批准修订版 → 实施落地（Sub + 表测试 + go.mod，go test/gofmt/vet 全过）。
- 状态库：`fb_actions` = `exit_plan_mode|denied|plan-review:delivered`（第一张）→ `exit_plan_mode|approved`（修订版）；事件链 `approval_resolved(review_requested)` → `plan_reviewed(done, 78632ms)` → `approval_resolved(denied, reason=marker, confirmation=回传句)` → `approval_resolved(approved)`。
- `fb_messages` id 28（denial tool result）：内容含指令头「The plan review you asked for has returned. … call exit_plan_mode again…」+ `<review model="zhipuai / glm-5.3-flash">` 评审正文 + Verdict；**零** marker 串、零 "outranks the reviews" 段。

**场景 B（批准中止在途评审，gateway web 路径）**：`forebrain gateway start`（隔离 home2，auth=none）+ `/ws/chat` 驱动 → enter_plan_mode 审批（approve）→ exit_plan_mode 审批 → POST `/actions/:id/plan-review`（202）→ 评审 started 后 POST `/actions/:id/approve`：
- `plan_reviewed` **outcome=stopped**（评审被批准中止）；exit_plan action 保持 **approved**（用户决定保持最后话语权）；reviewer 自身的 pending shell 门被连带干净撤销（`the subagent approval was abandoned before a decision was returned`）。
- 说明：批准后实施尾巴因 WS 测试驱动客户端断连而未跑完（run done、无实施文件）——是驱动退出所致，非本特性缺陷；取消语义全部由服务端持久证据证明。

**场景 C（回归：无评审手动拒绝）**：同会话第二轮计划 → 浮层选 3「No, keep planning」→ 输入「改成先覆盖溢出场景的说明再提交」→ denial tool result（id 58）`User feedback:` 段**只有用户话语**，无指令头、无评审段；`tool_display` body = 用户话语原文。与 id 28（回传）并排对照，两路径互不污染。

**场景 D（中断自救）**：第二轮评审回传、planner 修订进行中 Ctrl+C → 「cancelling run」干净收尾；再 Ctrl+C 退出应用 **EXIT=0**；`forebrain resume <sid>` 重放 53 条消息，评审卡、回传 denial 记录、修订中途的思考全部在场，无孤儿审批（fb_actions 无 pending），composer 可继续。

**显示缺口修复复验（偏差 4）**：重建二进制后新一轮完整回传 → 新 denial 行（id 118）`tool_display` body =「Plan review delivered — the planner is revising the plan.」（修复前同型行 id 86 = "(no output)"）；该 body 即重放渲染源（与场景 C 手动拒绝行同一渲染路径，已目验）。

### 命令与结果

- `CGO_ENABLED=1 go test -tags fts5 ./... -count=1` → **exit 0，29 包 ok**（实施后两轮全跑）。
- `gofmt -l pkg cmd` → 空；`go vet ./...` → 0。
- `cd frontend && corepack pnpm test` → **312/312 过**（40 文件）；`corepack pnpm exec vue-tsc --noEmit -p tsconfig.json` → 0 错。
- `scripts/package-graph.sh` → 仅 LOC 计数变化，fan_in/fan_out 边零变化（pkg/run 16 出边不变；文案常量放在 pkg/tool，未给 pkg/run 加 pkg/turn 依赖）。
- gateway 5 连跑（flake 收口前 1/3 失败 → 收口后 5/5 ok）。

### 与计划的偏差（4 条，均有理由）

1. **frontend 未做 locale 键映射**：web 时间线 ApprovalCard 按既有契约只回放 `confirmation`（表面打印的原句），不渲染 reason。故引擎在回传的 approval_resolved 事件上带 `Confirmation`（与 TUI 打印同句，常量 `turn.PlanReviewDeliveredText`），web 零生产代码改动即与 TUI 1:1；locale 键反而会造出第二套文案并与 Confirmation 双显。计划里 frontend 的改动收敛为：useChatStream 测试（回传行落块）+ 新增 ApprovalCard.test.ts（显示回传句、绝不显示 marker）。
2. **文案常量位置**：`PlanReviewDeliveredText` 源头在 `pkg/tool/format.go`（display 层，与 DeniedToolDisplayBody 同处），`pkg/turn` 以 const 别名再导出——因为 pkg/run（orchestration）也要用它且不能引 pkg/turn。
3. **gateway 测试基建**：评审完成用 `Runner.SubagentExecutor` 桩（新增 fakeGatewaySubagentExecutor）；批准/回传测试预先 `ClearWait` 让后台 resume goroutine 在第一步返回——全量跑发现的 TempDir 清理竞态（goroutine 在 claim 前有 state.Switch 写盘），根因修复而非 sleep 掩盖。
4. **计划外新增：持久 denial display body 修复**：真机场景 A 取证发现回传 denial 行的 `tool_display` body 为 "(no output)"（DeniedToolDisplayBody 对空 feedback 的既有行为被本特性触发）。根因修复：`ToolApprovalResumeState`/`turn.ApprovalResume` 增加 `DeliveredReview` 布尔（TUI/gateway 装配处从 BuildApprovalResumeWithPlanReviews 传入），orchestration 写 display body 时用回传句；配套 `TestDeliveredReviewDenialCarriesTheHandoffLine`（pkg/run）+ 既有测试补断言；真机复验通过（见上）。此为本次变更引入缺口的闭环修复，属 in-scope。

### 遗留

- 场景 B 的 WS 驱动是临时 python 脚本（/tmp/forebrain-prd/ws_drive.py），未沉淀为 skill/脚本；复用时照验收记录重建。
- 修复前已持久化的 "(no output)" denial 行（真机 home 的 id 86 等历史行）不会追溯改写——按 append-only 契约不动存量。

## STOP conditions

- TUI 评审 done 后"不再重提示原审批"牵出大面积浮层/e2e 断言重写（超出本文矩阵的行为变化）→ 停下报告，不得顺手改交互设计。
- `BuildApprovalResumeWithPlanReviews` 白名单之外发现 `action.Error`/`Feedback` 的其他消费点会被标记污染（display golden、迁移测试）→ 列全清单后再继续；无法白名单收尾 → 停。
- 回传恢复出现双重 denial 注入（denied resume 与新审批并发竞争同一 parked run）→ 停下报告时序证据。
- 主会话缓存命中率较基线下降（回传引入了前缀分叉）→ 停。

## Maintenance notes

- `PlanReviewDeliveredReason` 是跨 TUI/web/回放的显示契约：改字符串需三端同步（引擎白名单、denial 卡、web locales）。
- 未来若引入"批准原计划（忽略评审）"入口，批准中止评审逻辑（本计划 §2/§3）就是它的挂点；不要另起通道。
- 评审回传与用户手动拒绝共用 denied-resume：任何 denial 注入语义变更（`buildDenialMessage`、`consumeResumeSnapshot`）都同时影响两者，评审回传的 golden 必须随改。

## Implementation Status（answer-stamp 判别回填，2026-10-08）

> 本段记录判别机制的现状：Fix design §1 计划的裸 reason-marker 白名单在落地中被「marker + 结构化 answer stamp 双读数」取代。上方 Acceptance record 的 4 条偏差未覆盖此项（该记录与全文均无 `DenyWithAnswer` / stamp 的记载，grep 零命中，已核对），本段补记；计划正文与验收记录保持历史原样，判别机制以本段为准。

### 落地内容

| 组件 | 位置（已 grep 核对） | 现状 |
|---|---|---|
| 投递写路径 | `pkg/turn/approval.go:170` `DeliverPlanReview` | 唯一写点：`Get` 后校验 `Status==pending` 且 `Kind==exit_plan_mode`（`:174-181`），否则返回 `state.ErrNotPending`；随后 `actions.DenyWithAnswer(ctx, actionID, PlanReviewDeliveredReason, planReviewDeliveredAnswer)`（`:182`） |
| CAS + stamp | `pkg/state/action_service.go:298` `DenyWithAnswer` | pending→denied 的 CAS（`UPDATE fb_actions … WHERE id=? AND status=pending`，`:307-310`；`RowsAffected==0` → `ErrNotPending`，`:316-317`），同写 answer stamp；其唯一生产调用点是 `DeliverPlanReview`——用户 deny 走 `Deny`，不带 stamp |
| marker 常量 | `pkg/turn/approval.go:1092` | `PlanReviewDeliveredReason = "plan-review:delivered"`——显示契约，判别条件的一半 |
| stamp 常量 | `pkg/turn/approval.go:1097` | `planReviewDeliveredAnswer = {"plan_review_delivered":true}`——只有引擎写路径能设置 |
| 双条件谓词 | `pkg/turn/approval.go:1104` `ActionIsReviewDelivered` | marker（`action.Error`）+ stamp（`answer_json` 解出 `plan_review_delivered:true`）缺一不可；生产消费两处：resume 组装（`approval.go:151`）与 TUI denial 卡（`pkg/tui/chat_turn.go:1071`）；gateway 经 `gatewayResumeState`（`pkg/gateway/api_extra.go:3069`，`DeliveredReview` 字段级透传 `:3075`） |
| resume 组装 | `pkg/turn/approval.go:140-159` `BuildApprovalResumeWithPlanReviews` | 谓词命中 → `Feedback=""`、`Reason=ComposeReviewDeliveryGuidance(results)`、`DeliveredReview=true`（`:151-155`）；未命中仍是 `ComposeDenyFeedback` 拼用户话语 |
| 指令头 | `pkg/turn/approval.go:1125,1133` | `reviewDeliveryGuidanceHeader` 为编译期常量（prompt 前缀缓存稳定）；`ComposeReviewDeliveryGuidance` 只拼指令头+评审，无用户话语槽 |
| gateway 投递入口 | `pkg/gateway/api_extra.go:2875` `deliverGatewayPlanReview` | `ErrNotPending` 静默返回（用户赢了竞态）；发布 `approval-resolved`（`Confirmation: turn.PlanReviewDeliveredText`，`:2903`）；`go s.resumeGatewayRun(act.ID, false)` denied-resume 派发（`:2906`） |
| TUI 投递入口 | `pkg/tui/chat_surface.go:931` `deliverCompletedPlanReview` | 同构：`ErrNotPending` 返回；`approval-resolved` 的 `Confirmation`（`:965`）与系统提示行 `Content`（`:970`）均为 `PlanReviewDeliveredText`；`s.retryResumeDispatch(act.ID, "deny", s.resumeAfterDenial)`（`:973`） |
| 批准中止在途评审 | `cancelInFlightPlanReview`：gateway `pkg/gateway/api_extra.go:2913`、TUI `pkg/tui/chat_surface.go:980` | 调用点各两处、均限 `exit_plan_mode` 的 approve：幂等重批（gateway `:3025` / TUI `:503`）与首次批准（gateway `:3041` / TUI `:541`）；best-effort——评审先完成则投递撞 `ErrNotPending` 自然 no-op |

### 与计划的偏差（有据修订）

Fix design §1 原文的两个判别/写点设计被取代：

1. **字符串白名单 → marker + stamp 双读数**。计划：`BuildApprovalResumeWithPlanReviews` 加白名单 `out.Feedback == PlanReviewDeliveredReason`。现状：判定函数是 `ActionIsReviewDelivered`（marker + 结构化 stamp 缺一不可）。理由：用户可能把 marker 串逐字打成自己的拒绝理由，裸字符串匹配会把用户话语误判为引擎投递（Feedback 被错误置空、模型错误收到"无用户话语"的回传语义）；stamp 只有 `DeliverPlanReview` 的 `DenyWithAnswer` 写路径能设置，手打设不出去。
2. **裸 `actions.Deny(reason)` → `DenyWithAnswer(reason, stamp)` CAS**。计划：`actions.Deny(ctx, actionID, PlanReviewDeliveredReason)`，返回 deny 后的 action。现状：`DenyWithAnswer` 在 CAS 关闭的同时写入 stamp（`action_service.go:298`）。

裸 marker（无 stamp）的存量/人工路径行为：**不被识别为投递**，按用户自己的拒绝话语处理（`ComposeDenyFeedback` 拼入）。判别器断言锚点：`TestDeliverPlanReviewStampsTheAnswer`（`pkg/turn/approval_test.go:832`）——manual `Deny(id, PlanReviewDeliveredReason)` 后 `ActionIsReviewDelivered` 必须为 false（`:861`）。

### 验证

| 检查 | 位置 | 断言/结果 |
|---|---|---|
| `TestPlanReviewAutoDeliversAndALateUserDenyConverges` | `pkg/tui/chat_session_test.go:12492` | 评审 done 自动回传（status=denied、error=marker）、审批门不再重提示；用户迟到的 deny 幂等收敛、不改写已投递 marker。反方向（用户先决策）由投递侧 `ErrNotPending` CAS no-op 收敛——「用户决策最后发言权」两向闭环 |
| `TestPlanReviewAutoDeliversToPlannerAfterDone` | `pkg/tui/chat_session_test.go:12605` | 审批以回传 deny 关闭，恢复输入以指令头开头并携带评审，marker 与用户从未打过的话语都不外泄 |
| `TestGatewayResumeStateCarriesDeliveredReview` | `pkg/gateway/api_extra_test.go:3976` | `gatewayResumeState` 字段级携带 `DeliveredReview`/`Denied`/`DenyReason`/`DenyFeedback`/`Session`， parked run 不丢字段 |
| `shows a delivered plan review as the handoff line, never the marker` | `frontend/src/composables/useChatStream.test.ts:2002` | web 时间线回放回传句，绝不显示 marker 串 |
| `replays the delivered line for a review handed back to the planner` | `frontend/src/components/chat/ApprovalCard.test.ts:27` | denial 卡回放回传句；`:36` 断言文本不含 marker |
| `TestDeliverPlanReviewStampsTheAnswer` | `pkg/turn/approval_test.go:832` | 判别器：引擎投递路径命中；手打 marker（无 stamp）不命中 |
| `go vet ./...` | 回填时复跑 | exit 0，无输出 |

### 未做 / 遗留

none——本次回填逐项核对了上表全部符号、file:line 与测试锚点（grep/亲读均命中），未发现机制与记录的空缺。
