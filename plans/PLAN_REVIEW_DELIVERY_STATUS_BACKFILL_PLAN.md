# Plan 7: 回填 PLAN_REVIEW_DELIVERY_PLAN 的 Implementation Status（记录 answer-stamp 对 reason-marker 的取代）

> **Executor instructions**: Follow this plan step by step. Run every
> verification command and confirm the expected result before moving to the
> next step. If anything in the "STOP conditions" section occurs, stop and
> report — do not improvise. When done, update the status row for this plan
> in `plans/README.md` — unless a reviewer dispatched you and told you they
> maintain the index.
>
> **Drift check (run first)**:
> `git diff --stat 6667bb5 -- pkg/turn/approval.go pkg/state/action_service.go docs/plan/PLAN_REVIEW_DELIVERY_PLAN.md`
> 本计划写于**有未提交改动的工作区**（commit 6667bb5 + dirty tree）。以
> "Current state" 的 file:line 与符号名对照磁盘为准；不符即 STOP。

## Status

- **Priority**: P3（文档收口；不影响运行行为）
- **Effort**: S
- **Risk**: LOW（纯文档追加，零代码改动）
- **Depends on**: none
- **Category**: docs
- **Planned at**: commit `6667bb5` + 工作区 dirty（2026-10-08，工作树 diff 审计发现 #2）

## Why this matters

`docs/plan/PLAN_REVIEW_DELIVERY_PLAN.md` 记录了"评审完成自动回传 planner"特性
的设计，但其 §设计描述的落点（`out.Feedback == PlanReviewDeliveredReason`
字符串白名单 + `actions.Deny(ctx, actionID, PlanReviewDeliveredReason)`）与实际
落地的机制**不一致**：实际实现为防止"用户把 marker 字符串当自己的拒绝理由手
打进去"被误判，改用了 `DenyWithAnswer` 结构化 answer stamp 做双重判别。计划文
档没有 Implementation Status 段，落地偏差无记录。下一位维护者按这份文档理解或
测试该特性，会得到一个比现实更弱的契约（以为裸 marker 匹配就是判别条件）。
本仓库的惯例是计划文件收口时回填实施状态（同日另一计划
`docs/plan/SUBAGENT_VIEW_WHEEL_SCROLL_RUNAWAY_PLAN.md` §10 即范例形态）。

修复后：该计划文档自带"计划了什么 / 实际落了什么 / 为何偏差 / 怎么验证的"
完整闭环，成为该特性可信的第一手资料。

## Current state

- `docs/plan/PLAN_REVIEW_DELIVERY_PLAN.md` — 待回填对象。已核实：
  - 文件内**没有** "Implementation Status" 段（`grep -n 'Implementation Status'`
    零命中），也没有 `DenyWithAnswer` / `planReviewDeliveredAnswer` / answer
    stamp 的任何提及。
  - §设计中的计划机制（引用原文两处）：
    - "`BuildApprovalResumeWithPlanReviews`（`:129-142`）加白名单：
      `out.Feedback == PlanReviewDeliveredReason` 时先置 `out.Feedback = \"\"` 再组合"
    - "`actions.Deny(ctx, actionID, PlanReviewDeliveredReason)`，返回 deny 后的 action。"
- 实际落地机制（回填时引用的事实，全部已核实存在于工作区）：
  - `pkg/turn/approval.go:170` `DeliverPlanReview` — 唯一投递写路径：双读数
    （Get 后校验 pending + kind==exit_plan_mode）→ `DenyWithAnswer`。
  - `pkg/turn/approval.go:1092` `PlanReviewDeliveredReason = "plan-review:delivered"`
    （显示契约 marker，仅一半判别条件）。
  - `pkg/turn/approval.go:1097` `planReviewDeliveredAnswer = {"plan_review_delivered":true}`
    （结构化 stamp，**只有引擎写路径能设置**，用户手打 marker 串设不上）。
  - `pkg/turn/approval.go:1104` `ActionIsReviewDelivered` — marker + stamp 双
    条件谓词，三处消费（resume 组装、TUI 卡片、orchestration 显示）。
  - `pkg/turn/approval.go:1133` `ComposeReviewDeliveryGuidance` — 指令头为编译
    时常量（prompt 前缀缓存稳定）。
  - `pkg/state/action_service.go:298` `DenyWithAnswer` — pending→denied 的 CAS
    且同写 answer stamp（取代计划的裸 `Deny(reason)`）。
  - 两个 surface 的投递入口：`pkg/gateway/api_extra.go:2875`
    `deliverGatewayPlanReview`；`pkg/tui/chat_surface.go:931`
    `deliverCompletedPlanReview`；`api_extra.go:3069` `gatewayResumeState`
    字段级投影（`DeliveredReview` 透传）。
  - 验证锚点（回填"Verification"用）：`pkg/tui/chat_session_test.go:12492`
    `TestPlanReviewAutoDeliversAndALateUserDenyConverges`、`:12605`
    `TestPlanReviewAutoDeliversToPlannerAfterDone`、
    `pkg/gateway/api_extra_test.go:3976`
    `TestGatewayResumeStateCarriesDeliveredReview`、
    `frontend/src/composables/useChatStream.test.ts`（"shows a delivered plan
    review as the handoff line, never the marker"）、
    `frontend/src/components/chat/ApprovalCard.test.ts`（delivered-line replay）。
- 回填文案的语言与文风：该计划文档为中文；追加段沿用中文、沿用同文件既有
  表格/小节风格。范例形态参考 `docs/plan/SUBAGENT_VIEW_WHEEL_SCROLL_RUNAWAY_PLAN.md`
  的 "## 10. 验收回填" 段（标题结构、表格、偏差小节）。

## Commands you will need

| Purpose | Command | Expected on success |
|---|---|---|
| 事实核对 | `grep -n 'Implementation Status' docs/plan/PLAN_REVIEW_DELIVERY_PLAN.md` | 回填前零命中；回填后 1 命中 |
| 符号存在性 | `grep -n 'func DeliverPlanReview\|func ActionIsReviewDelivered\|planReviewDeliveredAnswer =' pkg/turn/approval.go` | 三处命中，行号与 Current state 一致（±若工作区又漂移则按 STOP 处理） |
| 测试锚点存在性 | `grep -n 'func TestPlanReviewAutoDelivers' pkg/tui/chat_session_test.go` | ≥2 命中 |

## Scope

**In scope（只允许改这一个文件）**：
- `docs/plan/PLAN_REVIEW_DELIVERY_PLAN.md` — 文件末尾追加一个
  `## Implementation Status`（或与文件既有编号风格一致的等价标题）小节。

**Out of scope（明确不碰）**：
- 任何 `.go` / `.ts` 文件 — 本计划零代码改动。
- 计划文档的既有正文（§设计等）— 历史送审记录保持原样，偏差只在 Status 段
  说明，**不回头改写计划正文**。
- `plans/` 下其它计划文件（它们各自的执行记录已存在于 `plans/README.md`）。

## Git workflow

- 改动留在工作区，**禁止 git commit**（owner 手动逐个审阅提交，仓库纪律）。

## Steps

### Step 1: 核对事实（先跑 Commands 表的三条核对命令）

确认 Current state 列出的符号、行号、测试名在磁盘上存在。行号若有小幅漂移
（工作区后续提交所致），以符号名为准并在回填中写符号名+行号；机制与计划正
文的偏差描述必须以你亲读的代码为准，**不要照抄本计划的事实清单而不核对**。

**Verify**: 三条命令均有预期命中。

### Step 2: 追加 Implementation Status 段

在 `docs/plan/PLAN_REVIEW_DELIVERY_PLAN.md` 文件**末尾**追加（内容要点，措辞
可润色，事实不得增删）：

1. **落地内容**：DeliverPlanReview 单一写路径 + DenyWithAnswer CAS + answer
   stamp；ActionIsReviewDelivered 双条件谓词驱动 resume 组装（Feedback 置空、
   Reason=ComposeReviewDeliveryGuidance）与两 surface 显示；gateway/TUI 投递
   入口、approval-resolved 事件（Confirmation=PlanReviewDeliveredText）、
   denied-resume 派发；批准（含幂等重批）中止在途评审。
2. **与计划的偏差（有据修订）**：计划的 `out.Feedback == PlanReviewDeliveredReason`
   字符串白名单与 `actions.Deny(reason)` 被"marker + 结构化 answer stamp 双读
   数"取代——理由：用户可能字面手打 marker 串作为自己的拒绝理由，裸字符串匹
   配会把用户话语误判为引擎投递；stamp 只有 `DeliverPlanReview` 的写路径能设
   置。裸 marker（无 stamp）的存量/人工路径行为：不被识别为投递。
3. **验证**：列出 Current state 给出的五个测试锚点（两 TUI、一 gateway、两
   frontend）+ `go vet ./...` 干净；说明"用户决策最后发言权"由
   `ErrNotPending` CAS 收敛（引用 chat_session_test.go:12492 的测试名）。
4. **未做/遗留**：none（或如实写明执行者核对后发现的空缺——没有就写 none）。

**Verify**: `grep -n 'Implementation Status' docs/plan/PLAN_REVIEW_DELIVERY_PLAN.md`
→ 恰 1 命中；`tail -5 docs/plan/PLAN_REVIEW_DELIVERY_PLAN.md` 显示新段内容。

### Step 3: 收尾自查

**Verify**:
- `git status --porcelain -- docs/plan/PLAN_REVIEW_DELIVERY_PLAN.md` → 仅该文件
  一个 M（此前已是 untracked/新增文件的话则保持 untracked 状态不变）。
- `git diff` 中没有任何 `.go`/`.ts` 变更来自本任务。

## Test plan

- 纯文档，无新测试。Step 1 的事实核对命令即质量门：回填中的每个 file:line
  断言都必须先经 grep 验证存在。

## Done criteria

- [ ] `docs/plan/PLAN_REVIEW_DELIVERY_PLAN.md` 末尾存在 Implementation Status
      段，含"落地内容 / 与计划的偏差 / 验证 / 未做"四部分
- [ ] 偏差部分明确写出"字符串白名单 → marker+stamp 双读数"及其理由
- [ ] 回填引用的所有 file:line 与符号经 Step 1 grep 核对存在
- [ ] 零代码文件改动（`git status` 核对）
- [ ] `plans/README.md` 状态行更新

## STOP conditions

- Step 1 任一核对命令零命中（符号不存在=工作区已大漂移，本计划的事实基础
  失效）。
- 发现实际机制与本计划 Current state 描述不符（例如 stamp 谓词又变了）——停下
  汇报，勿按过时事实回填。
- 发现该文档已存在 Implementation Status（他人已回填）——勿重复追加，汇报即可。

## Maintenance notes

- 本回填让 `docs/plan/PLAN_REVIEW_DELIVERY_PLAN.md` 与代码重新对齐；后续任何
  对投递机制（stamp/谓词/CAS）的改动都应同步更新该 Status 段，而不是只改代码。
- 评审重点：偏差描述是否准确区分"计划正文（历史送审）"与"实际落地（现状）"，
  且没有回头改写计划正文。
