# Plan: gateway 评审回传可观测性 + resume 装配契约测试（GATEWAY_DELIVERY_OBSERVABILITY）

## Status

- **Priority**: P2（审计 #1）
- **Effort**: S
- **Risk**: LOW（一条日志 + 一个纯函数抽取与其单测；无行为变更）
- **Depends on**: PLAN_REVIEW_DELIVERY 已落地（本仓库工作区未提交状态即为该基线）
- **Category**: 可观测性 + 测试覆盖
- **标签**: introduced（PLAN_REVIEW_DELIVERY 引入的缺口）

## 背景（自包含）

PLAN_REVIEW_DELIVERY 特性让完成的计划评审自动回传 planner：评审 `done` 后，
gateway 的后台 goroutine（`handleActionPlanReview` 尾部）调
`Server.deliverGatewayPlanReview`，它经 `turn.DeliverPlanReview` 把 pending 的
exit_plan 审批以标记 reason（`plan-review:delivered`）内部 deny，发布
`approval_resolved` 事件，再 `go s.resumeGatewayRun(...)` 走 denied-resume 把
评审正文注入 planner。

## 问题（file:line 证据，均已开码核对）

1. **回传失败静默吞没**：`pkg/gateway/api_extra.go` `deliverGatewayPlanReview`
   中：
   ```go
   act, err := turn.DeliverPlanReview(ctx, s.Actions, actionID)
   if err != nil || act == nil {
       // ErrNotPending is the race the user won: their decision stands.
       return
   }
   ```
   注释只解释了 `ErrNotPending`（用户决策竞态，静默正确），但**其他错误同样静默**。
   对照 TUI 同职责函数 `pkg/tui/chat_surface.go` `deliverCompletedPlanReview`：
   它区分了 `errors.Is(err, state.ErrNotPending)`（静默）与其他错误
   （`s.chatLog.Errorf("forebrain tui plan_review deliver failed ...")`）。
   影响：DB 瞬断/损坏时，评审已完成（`plan_reviewed outcome=done` 已落库）但
   回传失败，无任何 trace——TUI 侧有日志可查，gateway 侧（web 用户）无迹可寻。

2. **`DeliveredReview` 装配零覆盖**：`pkg/gateway/api_extra.go` `resumeGatewayRun`
   内（约 :3170-3178）：
   ```go
   resumeState := &tool.ToolApprovalResumeState{
       Session:      resume.Session,
       Denied:       resume.Denied,
       DenyReason:   resume.Reason,
       DenyFeedback: resume.Feedback,
       // A delivered review has no user words: the persisted denial's
       // display half says the handoff line instead.
       DeliveredReview: resume.DeliveredReview,
   }
   ```
   `DeliveredReview` 标志决定持久 denial 的 `tool_display` body 是回传句还是
   `"(no output)"`（`pkg/run/orchestration_llm.go` denial 写入处消费）。链路
   两端都有测试（`pkg/turn` 的 resume→flag、`pkg/run` 的 flag→body），但
   **gateway 这一跳字段拷贝无测试**：未来重构删掉该字段，web 路径的持久
   denial body 静默回退 `"(no output)"`，CI 不拦。TUI 侧同型装配
   （`pkg/tui/chat_turn.go` `resumeAgentContext`）有
   `TestPlanReviewAutoDeliversToPlannerAfterDone` 断言覆盖。

## Fix design

### 1. 失败日志（对齐 TUI 语义）

`deliverGatewayPlanReview` 改为（与 TUI 侧结构完全同构）：

```go
act, err := turn.DeliverPlanReview(ctx, s.Actions, actionID)
if errors.Is(err, state.ErrNotPending) {
    return // 用户决策竞态：静默正确
}
if err != nil || act == nil {
    slog.Error("deliver plan review to planner failed",
        "action_id", actionID, "err", err)
    return
}
```

`ErrNotPending` 保持静默；其余错误（含异常的 nil action）一条日志。`slog` 该文件
已引入。

### 2. 装配纯函数抽取 + 单测

把 `resumeGatewayRun` 里 `resumeState := &tool.ToolApprovalResumeState{...}`
字面量抽为同文件纯函数：

```go
// gatewayResumeState projects one canonical approval resume into the resume
// state the parked run executes under. Field-for-field: anything the engine
// adds to turn.ApprovalResume must land here, or the surface silently drops
// it — the DeliveredReview copy is exactly such a field.
func gatewayResumeState(resume turn.ApprovalResume) *tool.ToolApprovalResumeState {
    return &tool.ToolApprovalResumeState{
        Session:         resume.Session,
        Denied:          resume.Denied,
        DenyReason:      resume.Reason,
        DenyFeedback:    resume.Feedback,
        DeliveredReview: resume.DeliveredReview,
    }
}
```

`resumeGatewayRun` 原地改用 `resumeState := gatewayResumeState(resume)`
（fence 绑定 `turn.BindApprovalContinuationFence(resumeState, ...)` 与
`tool.WithToolApprovalResume(ctx2, resumeState)` 不动）。

不抽 TUI 侧同型代码（它有 denyReason 局部变量、装配点内联在
resumeAgentContext 的大函数里且有测试覆盖；跨包抽共享 helper 会为单调用点
造抽象）。

## Steps

1. **失败测试先行**（`pkg/gateway/api_extra_test.go` 或新文件）：
   `TestGatewayResumeStateCarriesDeliveredReview`：构造
   `turn.ApprovalResume{DeliveredReview: true, Denied: true, Reason: "...",
   Feedback: "", Session: ...}` → `gatewayResumeState` 逐字段相等断言
   （重点 `DeliveredReview`）。先 FAIL（函数不存在）。
2. 实现抽取 + 改 `resumeGatewayRun` 调用点。转绿。
3. 日志改动 + 编译过。
4. 回归：`CGO_ENABLED=1 go test -tags fts5 ./pkg/gateway -count=1` → ok；
   `gofmt -l pkg cmd` 空；`go vet ./...` 0。

## Verification

- 新单测存在且绿。
- `CGO_ENABLED=1 go test -tags fts5 ./pkg/gateway ./pkg/turn ./pkg/tui -count=1` ok。
- 手工抽查（可选）：临时在 `DeliverPlanReview` 前置一个坏 DB 句柄不现实，
  日志路径靠 code review 即可（改动 5 行）。

## Out of scope

- TUI 侧装配测试（已有）。
- 重试逻辑（回传失败不重试：评审事件还在，用户可在 web 重开评审；本计划只保证
  失败可见）。

## STOP conditions

- 抽取牵出 `resumeGatewayRun` 更大重构需求 → 停在纯函数抽取，不扩面。
