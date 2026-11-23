# Plan: 评审回传标记与用户话语的碰撞加固（PLAN_REVIEW_MARKER_COLLISION）

## Status

- **Priority**: P3（审计 #3；机制 HIGH 置信、触发概率极低——按边缘正确性排期）
- **Effort**: M
- **Risk**: MED（触碰刚建立的回传契约三端显示面；靠双读数设计与全量 golden 兜底）
- **Depends on**: PLAN_REVIEW_DELIVERY 已落地（工作区未提交状态即该基线）
- **Category**: bug（边缘输入下静默丢用户话语）
- **标签**: introduced

## 背景（自包含，回传机制速览）

PLAN_REVIEW_DELIVERY 让完成的评审自动回传 planner：`turn.DeliverPlanReview`
把 pending 的 exit_plan 审批以 `error = "plan-review:delivered"`
（常量 `turn.PlanReviewDeliveredReason`，下称**标记**）内部 deny。恢复组装
`turn.BuildApprovalResumeWithPlanReviews`（`pkg/turn/approval.go` 约 :1085-1095）
凭**单读数**识别回传：

```go
if out.Feedback == PlanReviewDeliveredReason {   // Feedback = strings.TrimSpace(action.Error)
    out.Feedback = ""
    out.Reason = ComposeReviewDeliveryGuidance(results)
    out.DeliveredReview = true
    return out
}
```

标记同时是三端显示契约：`approval_resolved` 事件 `Reason` 字段、TUI denial 卡
（`notifyToolApprovalDenied` 判 `feedback == marker`）、持久 denial 的
`tool_display` body（`DeliveredReview` 标志）。

## 问题

`error` 列是**用户话语通道**：手动「No, keep planning」时用户输入的拒绝理由
原样写进同一列（`ActionService.Deny`，`pkg/state/action_service.go:270-292`）。
用户若**逐字输入标记串** `plan-review:delivered` 作为拒绝理由，恢复组装把它
误判为回传：用户话语被静默丢弃（`Feedback=""`）、模型输入被替换成回传指令头、
TUI 卡显示回传句而非用户原话。机制确定存在；触发需要用户恰好打出这串内部
plumbing 值（好奇/恶意/复制粘贴泄露），概率极低但行为错误。

同族隐患：`notifyToolApprovalDenied` 的卡文案判据同样是裸 `feedback == marker`。

## Fix design：双读数（dual-key）

保留 `error=标记` 不动（三端显示契约零变更），给**回传专用写路径**加第二个
读数——`answer_json` 标志。手动 deny 路径不写 `answer_json`，碰撞在结构上
不可能。

### 1. state 层：`DenyWithAnswer`

`pkg/state/action_service.go` 镜像 `ApproveWithAnswer`（:246-268）新增：

```go
// DenyWithAnswer atomically denies an action and records a structured marker
// alongside the denial reason. The plan-review delivery uses it to stamp the
// denial as engine-closed: the reason column stays the display contract, the
// answer column carries the machine discriminator a manual denial cannot set.
func (s *ActionService) DenyWithAnswer(ctx context.Context, id, reason, answerJSON string) (*Action, error)
```

同一条 UPDATE（`SET status=?, error=?, answer_json=?, updated_at=?
WHERE id=? AND status=?`），保持 pending→denied 的 CAS 语义。

### 2. `ApprovalStore` 接口扩展

`pkg/turn/approval.go` `ApprovalStore`（:58-64）加 `DenyWithAnswer` 方法。
实现者：`*state.ActionService`（上面新增）+ 测试桩 `approvalStoreStub`
（`pkg/turn/approval_test.go:21`，照 `ApproveWithAnswer` 桩样式补）。全仓
grep 过：接口仅在 pkg/turn 声明、仅此一个测试桩显式实现。

### 3. `DeliverPlanReview` 写双读数

```go
const planReviewDeliveredAnswer = `{"plan_review_delivered":true}`
...
return actions.DenyWithAnswer(ctx, actionID, PlanReviewDeliveredReason, planReviewDeliveredAnswer)
```

### 4. 单一判定谓词，三处消费

新增导出谓词（`pkg/turn/approval.go`）：

```go
// ActionIsReviewDelivered reports whether an action was closed by review
// delivery rather than by the user: both the reason marker (the display
// contract) and the structured answer stamp (the discriminator a manual
// denial cannot set) must be present. A user who literally types the marker
// as deny feedback has typed their own words, not this.
func ActionIsReviewDelivered(action *state.Action) bool {
    if action == nil || strings.TrimSpace(action.Error) != PlanReviewDeliveredReason {
        return false
    }
    var a struct {
        PlanReviewDelivered bool `json:"plan_review_delivered"`
    }
    return json.Unmarshal([]byte(strings.TrimSpace(action.AnswerJSON)), &a) == nil && a.PlanReviewDelivered
}
```

消费点替换：
- `BuildApprovalResumeWithPlanReviews`：`if out.Feedback == ...` →
  `if ActionIsReviewDelivered(action)`（注意此函数内 action 已判 nil）。
- TUI `notifyToolApprovalDenied`（`pkg/tui/chat_turn.go`）：先读 action 得
  `act`，用 `turn.ActionIsReviewDelivered(act)` 决定卡文案（该函数本来就为
  feedback 读了一次 action，复用同一行）。
- gateway `deliverGatewayPlanReview` / 事件发布：`Reason: act.Error` 保持标记
  原样（web 回放走 `Confirmation`，不消费 Reason 语义——已有测试锁住）。

### 5. 兼容性：存量回传行

合入前已持久化的回传 denial 行（真机验收产生的 id 118 等）只有 `error=标记`、
无 answer 标志。两个选项：
- **(a) 接受降级**（推荐）：存量行在 resume 组装时被当手动 deny——Feedback=
  标记串、Reason=ComposeDenyFeedback（评审仍随行，因为评审在事件里）。实际
  影响≈零：那些 run 已终结（wait 已清、不可再 resume），降级路径不可达；仅
  owner 本机几个验收会话持有此类行，非生产数据。
- **(b) 兼容读**：谓词放宽为「有 answer 标志，或（无 answer_json 且
  error==标记）」——这会重新打开碰撞窗口（手动 typed-marker 无 answer_json
  同样命中），**否决**。
- 结论：选 (a)，在计划验收记录里写明。若 owner 要求兼容，需另寻判据（如
  比对 action.updated_at 与 plan_reviewed 时序），超出本计划。

## Steps

1. **失败测试先行**：
   - `TestManualDenyTypingTheMarkerKeepsTheUsersWords`（pkg/turn）：
     action{denied, Error=标记, AnswerJSON=""}（手动 deny 的形态）→
     `BuildApprovalResumeWithPlanReviews` 断言 `Feedback=="plan-review:delivered"`
     （话语保留）、`Reason` 含该串（ComposeDenyFeedback 组合）、
     `DeliveredReview==false`。
   - `TestDeliverPlanReviewStampsTheAnswer`（pkg/turn）：
     `DeliverPlanReview` 后 action `AnswerJSON` 含标志、`Error` 仍为标记；
     `ActionIsReviewDelivered` true；仅 error=标记无 answer 时 false。
   - TUI：`TestDeniedExitPlanCardShowsOnlyTheUsersWords` 加一个变体——
     手动 typed-marker 的 denial 卡显示原话（不再是回传句）。
   - 验证：FAIL（谓词/方法不存在或行为不符）。
2. 实现 state → 接口 → 桩 → 谓词 → 三处消费。
3. 既有回传测试全量转绿（`TestDeliverPlanReviewDeniesPendingExitPlan`、
   `TestBuildApprovalResumeTreatsDeliveredReasonAsNoUserWords`、TUI/gateway
   回传用例——它们的 fixture 走 `DeliverPlanReview` 真路径，自动带标志；
   唯一手造 `Error=标记` 行的 fixture 需同步补 AnswerJSON）。
4. 回归：`CGO_ENABLED=1 go test -tags fts5 ./pkg/turn ./pkg/state ./pkg/tui
   ./pkg/gateway ./pkg/run -count=1` ok；全量 `./...` ok；gofmt/vet；
   `scripts/package-graph.sh` 无 diff。
5. 真机抽查（可选）：TUI 走一次评审回传，确认行为与合入前无差（显示契约
   未动）；手动 deny 输入标记串，确认话语保留。

## Verification

- 新测试 + 既有回传 golden 全绿；marker 裸串 `rg -n "plan-review:delivered"`
  仍只在常量定义与测试 fixture。
- 双读数不变量：手动 deny 路径（TUI `completeSurfaceApproval` →
  `ApprovalService.Decide` choice=deny → `Actions.Deny`）不写 answer_json——
  code review 确认无其他 `answer_json` 写点泄漏进 deny 路径。

## Out of scope

- 存量行迁移（选 (a)，不写迁移）。
- `DenyIfPending`（:382）不加 answer 参数——无回传调用方。

## STOP conditions

- 发现 `answer_json` 对 denied action 有既有消费方（解析失败即拒）→ 列清单
  评估后再继续；无法安全共存 → 停。
- 三端显示面出现标记串新泄漏点 → 停在白名单收尾原则。
