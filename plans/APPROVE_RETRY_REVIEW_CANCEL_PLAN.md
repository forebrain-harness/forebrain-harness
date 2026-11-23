# Plan: 批准重试路径也中止在途评审（APPROVE_RETRY_REVIEW_CANCEL）

## Status

- **Priority**: P2（审计 #4）
- **Effort**: S
- **Risk**: LOW（两个早 return 分支各加一次 best-effort 取消；无状态写入）
- **Depends on**: PLAN_REVIEW_DELIVERY 已落地（工作区未提交状态即该基线）
- **Category**: bug（边缘：幂等重试路径漏取消）
- **标签**: introduced

## 背景（自包含）

PLAN_REVIEW_DELIVERY 的 owner 拍板行为：「运行中批准 = 批准原计划 + 自动中止
在途评审」。批准路径在两个 surface 都加了 best-effort 取消：TUI
`completeSurfaceApproval` 与 gateway `resolveGatewayApproval`，在
`result.Approved && kind==exit_plan_mode` 时触发。

## 问题（file:line 证据）

取消调用插在**非幂等路径**上；而 `ApprovalService.Decide` 对已 resolved 的
action 返回 `Idempotent`，两个 surface 的 Idempotent 分支在取消点**之前**
早 return：

1. TUI `pkg/tui/chat_surface.go` `completeSurfaceApproval`：
   ```go
   if result.Idempotent {
       if result.Cancelled {
           s.retryResumeDispatch(actionID, "cancel", abort)
       } else if result.Denied {
           s.retryResumeDispatch(actionID, "deny", s.resumeAfterDenial)
       } else {
           s.retryResumeDispatch(actionID, "approve", s.resumeAfterApproval)
       }
       return nil            // ← 早 return，跳过下方 approved 分支的取消
   }
   ...
   if result.Approved {
       if strings.EqualFold(..., "exit_plan_mode") {
           s.cancelInFlightPlanReview()   // ← 只在非幂等路径到达
       }
       ...
   }
   ```
2. Gateway `pkg/gateway/api_extra.go` `resolveGatewayApproval` 同构：
   `if result.Idempotent { ...; return nil }` 在
   `if result.Approved && ...exit_plan_mode { s.cancelInFlightPlanReview(...) }`
   之前。

**影响**：批准的 HTTP/WS/浮层重试（决策已提交、续跑派发失败的恢复信号——注释
原话）撞 Idempotent 分支时，在途评审不被取消，跑完浪费一次模型调用。
正确性无损：评审完成后的回传 CAS 撞 `ErrNotPending` 自然 no-op（评审只留
事件）。这是资源浪费 + 语义不一致，不是数据错误。

## Fix design

两个 surface 的 Idempotent 分支，approved 情况补同一取消：

### TUI（chat_surface.go Idempotent 分支）

```go
if result.Idempotent {
    // An approved answer's retry is still an approval: it ends any review
    // still running against the plan, exactly as the first response did.
    if result.Approved && strings.EqualFold(strings.TrimSpace(act.Kind), "exit_plan_mode") {
        s.cancelInFlightPlanReview()
    }
    ...原三路 retryResumeDispatch...
    return nil
}
```

（`act` 在函数顶部已加载，kind 可用。）

### Gateway（resolveGatewayApproval Idempotent 分支）

```go
if result.Idempotent {
    if result.Approved && strings.EqualFold(strings.TrimSpace(pending.Kind), "exit_plan_mode") {
        s.cancelInFlightPlanReview(ctx, sid, actionID)
    }
    ...原逻辑...
    return nil
}
```

（`pending` 同样在函数顶部已加载。）

两处维持 best-effort 语义：取消失败只记日志（gateway 侧已有 slog.Error；
TUI 侧 cancelInFlightPlanReview 无返回值），竞态输给评审完成由回传 CAS 收敛。

## Steps

1. **失败测试先行**：
   - TUI（`pkg/tui/chat_session_test.go`，以 `newPlanReviewSession` 为底）：
     `TestIdempotentApproveStillCancelsInFlightReview`：
     1. `actions.Approve(ctx, actionID, "web")` 先落决策（制造 Idempotent 态）；
        同时按 fixture 惯例 `runs.ClearWait` 防后台 resume 竞态 TempDir 清理。
     2. `cs.trackPlanReviewCancel(func(){ close(cancelled) })` 模拟在途评审。
     3. `cs.completeSurfaceToolApprovalDecision(ctx, actionID, {Approved: true})`
        → 断言 `cancelled` channel 关闭。
   - Gateway（`pkg/gateway/api_extra_test.go`）：复制
     `TestGatewayApproveCancelsAnInFlightPlanReview` 的搭建（started 事件 +
     subagent_spawned 事件 + `agent.RegistryFor(...).Start(..., cancelFn)` +
     `runs.ClearWait` 防竞态），但先 `actions.Approve` 再走
     `handleActionsApprove` HTTP 路径 → 断言 cancelFn 触发、action 仍 approved。
   - 验证：两用例 FAIL。
2. 实现两处 Idempotent 补取消。转绿。
3. 回归：`CGO_ENABLED=1 go test -tags fts5 ./pkg/tui ./pkg/gateway -count=1` ok；
   gateway 连跑 3 次确认无 flake（该包有后台 goroutine 竞态前科，见
   PLAN_REVIEW_DELIVERY 验收记录偏差 3）。

## Verification

- 两条新测试绿；两个包级回归 ok；gofmt/vet 干净。
- 断言只依赖服务端持久/内存状态（cancelFn 触发、action 状态），不依赖时序 sleep。

## Out of scope

- Deny/Cancel 的 Idempotent 重试不需要取消评审（评审在 deny 场景本来就要跑完
  回传；cancel 场景 run 已终结）。

## STOP conditions

- Idempotent 分支内发现其他被跳过的 approved 副作用（不止评审取消）→ 列清单
  报告，不擅自扩面。
