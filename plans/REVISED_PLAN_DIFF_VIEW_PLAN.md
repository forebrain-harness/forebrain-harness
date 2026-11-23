# Plan: 修订版计划差异视图 — design/spike（REVISED_PLAN_DIFF_VIEW）

## Status

- **Priority**: P4（审计 D1，direction）
- **Effort**: M-L（本计划只做 spike 与设计裁决；实施另立计划）
- **Risk**: LOW（spike 只读 + 原型；实施风险在设计文档里评估）
- **Depends on**: PLAN_REVIEW_DELIVERY 已落地（工作区未提交状态即该基线）；
  建议在其收尾计划（observability / retry-cancel / marker-collision）合入后启动，
  行为基线更稳。
- **Category**: direction（产品增强）
- **标签**: direction（grounded）

## 为什么值得做（repo 自身证据）

PLAN_REVIEW_DELIVERY 落地后，评审回传驱动的修订闭环已真机验证：评审 done →
回传 → planner 修订 → 新 exit_plan 审批卡。但真机验收（2026-10-08，
docs/plan/PLAN_REVIEW_DELIVERY_PLAN.md 场景 A）显示：新审批卡展示的是修订后
计划全文 + planner 自己写进计划文档的「Adopted/Declined」叙述；**评审意见与
计划正文实际改动的对应要靠用户脑内 diff**。计划越长，这个人工比对越贵——
这正是回传特性创造出的新摩擦。

## 已核实的数据面事实（spike 的起点，均已开码核对）

1. **旧计划正文今天不存在于任何事件**：`PlanUpdatedPayload`
   （`pkg/event/run_events.go:452`）是 todo 进度
   （Title/Items/Completed/Active/AgentID），不是计划文件内容。
2. **`plan_review_started` 载荷**（`PlanReviewStartedPayload`）只带
   ActionID/ReviewID/Provider/Model/Label，无计划文本；`plan_reviewed` 带评审
   正文（Text），也不带计划文本。
3. **计划文件被原地覆写**：planner 修订即覆盖（`state.SetPlanForProject` /
   对计划路径的写），无版本化。
4. **审批卡已有计划正文通道**：TUI overlay 与 web approval-request
   （`plan_text` 字段）都携带当前全文；TUI overlay 还会渲染
   PlanReviews notes（评审意见卡在选项上方）。
5. **时序保证**：审批 pending 期间 planner 是 parked 的，计划文件不会变；
   「评审开始时的计划正文 == 回传关闭审批时的正文 == 被评审裁决的版本」，
   除非用户手工编辑计划文件（spike 需确认是否接受这个边角）。

## Spike 要裁决的问题（按序）

### Q1 快照来源（核心裁决）

- **方案 A（推荐起点）**：`plan_review_started` 载荷增加计划正文字段（经
  `turn.Truncate` 按 `MaxPlanChars` 有界截断，与评审提示词同界）。优点：
  快照即评审者裁决的原文；事件即存储，跨进程/重启天然可用；append-only
  契约不破。代价：事件体变大（与 plan_reviewed 的 Text 同量级，可接受）。
- 方案 B：计划文件版本化（state 层多版本）。重、动 schema，除非 A 被否否则
  不取。
- 方案 C：回传时刻快照（delivery 时另发事件）。与 A 等值（见事实 5）但多一个
  事件类型，劣于 A。
- 交付物：裁决 + `PlanReviewStartedPayload` 字段定义（含截断语义与
  「display-only，永不进模型输入」注记——缓存前缀是产品最高优先级，
  `docs/plan/PLAN_REVIEW_DELIVERY_PLAN.md` 行为矩阵同款约束）。

### Q2 新审批卡 → 快照的链接规则

回传后的新审批是新 action；旧评审事件挂在旧 actionID 上。候选规则（从会话
事件推导，无新状态）：

> 本 pending exit_plan 审批的 `approval_requested` 之前，同会话最近一次
> `approval_resolved(decision=denied, reason=plan-review:delivered)` 所关闭的
> action，其名下 `plan_review_started` 载带的正文即基线。

spike 验证该规则在多轮回传（评审→修订→再评审→再修订）下的正确性；如出现
歧义（用户手动 deny 后再提交等混合序列），明确降级行为（无基线 → 不显示
diff，绝不显示错基线）。

### Q3 展示形态与两端 parity

- TUI：审批浮层在计划正文上方增加「Changes since the review」段（行级
  diff，+/- 着色遵循现有配色裁决：克制纯色、禁粉、禁渐变）。
- Web：`approval-request` 载荷增加基线正文（或引擎侧预计算的结构化 diff——
  裁决：**下发原始两版正文，前端渲染 diff**，与 TUI 各自渲染、语义 1:1；
  若引入任何配置键，TUI/web 表单同计划交付——仓库常设 parity 裁决）。
- 文案：TUI 英文；web locale 中英同键。
- diff 算法：优先单文件 LCS/行 diff 小实现或既有依赖（spike 盘点
  `frontend/package.json` 与 Go 侧是否已有 diff 库；无则手写 ~80 行行级
  diff，避免为此引新依赖）。

### Q4 边界

- 无评审历史/无基线的手动流：不显示 diff（白名单收尾，不写不可达分支）。
- 基线与当前完全相同：不显示段。
- 超长计划：diff 段折叠行为与现有计划正文折叠一致。

## Steps（spike 本体，只读 + 原型，不改生产代码）

1. 盘点：`frontend/package.json` 依赖、Go 侧 diff 能力、`MaxPlanChars` 当前值
   与典型计划正文体量（真机验收会话的事件可作样本）。
2. 写设计文档 `docs/plan/REVISED_PLAN_DIFF_DESIGN.md`：Q1-Q4 裁决 + 载荷/
   API 字段定义 + 链接规则伪码 + 降级矩阵 + 实施工作量拆解（预估
   引擎 S / TUI M / web M）。
3. （可选，若裁决存疑）用真机验收会话的事件数据离线验证 Q2 链接规则。

## Verification（spike 的完成判据）

- 设计文档存在且每个 Q 有明确裁决与理由；能被一个未参与本会话的执行者直接
  转成实施计划。
- 无生产代码改动（`git status` 不因 spike 变脏，除设计文档）。

## STOP conditions

- Q1 裁决需要破坏 append-only 事件契约或引入 schema 迁移 → 停，回报选项。
- 发现快照正文有进入模型输入的任何路径 → 停（缓存前缀红线）。
