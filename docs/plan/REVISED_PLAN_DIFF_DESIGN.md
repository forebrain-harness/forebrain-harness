# 修订版计划差异视图 — 设计裁决（REVISED_PLAN_DIFF_VIEW spike 交付物）

对应计划：`plans/REVISED_PLAN_DIFF_VIEW_PLAN.md`。本文档是该 spike 的全部交付物：
Q1–Q4 逐项裁决、载荷/字段定义、链接规则伪码、降级矩阵、实施拆解。**spike 未改
任何生产代码**（`git status` 除本文档外无变化）。

## 0. 基线声明（本设计赖以成立的上下文）

- 基线 = 当前工作区未提交的 PLAN_REVIEW_DELIVERY 特性（评审 done → 回传 →
  planner 修订 → 新 exit_plan 审批卡）。
- 已落地的收尾计划：`plans/GATEWAY_DELIVERY_OBSERVABILITY_PLAN.md`（回传失败
  日志 + gatewayResumeState 抽取）、`plans/APPROVE_RETRY_REVIEW_CANCEL_PLAN.md`
  （幂等 approve 重试同时取消在途评审）。两者不改本设计依赖的事件契约。
- `plans/PLAN_REVIEW_MARKER_COLLISION_PLAN.md`（answer_json 双读数）**正在并行
  实施、本基线尚未包含**。交互见 §3.4：本设计的链接规则读
  `approval_resolved` 事件的 `reason` 字段（三端显示契约，该计划明确保留不
  动），不依赖其内部判据；该计划落地后，引擎内部识别回传的判据是
  `ActionIsReviewDelivered`（`error` = 标记 ∧ `answer_json` 站），与本规则正交。

## 1. 盘点结果（全部开码核对，spike 起点）

### 1.1 事件载荷现状

| 事实 | 证据 |
| --- | --- |
| `PlanReviewStartedPayload` 仅 ActionID/ReviewID/Provider/Model/Label，无计划文本 | `pkg/event/run_events.go:382-388` |
| `PlanReviewedPayload` 带评审正文 `Text`，无计划文本 | `pkg/event/run_events.go:392-402` |
| `PlanUpdatedPayload` 是 todo 进度（Title/Items/Completed/Active/AgentID），不是计划文件内容 | `pkg/event/run_events.go:452-469` |
| `RunPlanReview` 先发 `plan_review_started`、后读计划文件 | `pkg/turn/approval.go:1470-1474`（publish）、`:1487`（`state.GetPlanForProject`） |
| `approval_resolved` 载荷带 ActionID/ActionKind/Decision/Reason/Confirmation，且自述为 display history 而非 action 状态权威 | `pkg/event/approval_events.go:62-79` |
| 回传关闭：`DeliverPlanReview` 以 `error = "plan-review:delivered"` deny pending exit_plan | `pkg/turn/approval.go:159-179`、常量 `:1087` |
| 两端回传都发布 `approval_resolved(Decision=denied, Reason=act.Error)` | TUI `pkg/tui/chat_surface.go:957-967`；gateway `pkg/gateway/api_extra.go:2895-2905` |
| 事件存储：`fb_session_events(session_id, event_id, run_id, event_type, payload_json, occurred_at_ms)`，payload 为 JSON blob | `pkg/state/session_events.go:111` |
| 既有查询原语：`PlanReviewLog.ListSessionEventsOfType(ctx, sessionID, eventType, limit)`，`RunStore` 已实现 | `pkg/turn/approval.go:1213-1220` |

### 1.2 计划文件存储（对计划文本事实 3 的精确化）

`pkg/state/plan.go:1-14`：plans 目录按项目分目录，**每个计划是独立文件**，
「Historical plans are preserved: producing a new plan creates a new file rather
than overwriting the previous one」，"当前计划" = 目录里最新修改的 `.md`。
但 `SetForProject`（`pkg/state/plan.go:116-120`）写的就是
`PlanPathForProject` 返回的**当前最新文件**——即：新计划起新文件，而
**同一计划的修订（planner 按评审意见改写）是当前文件的原地覆写**。所以
「无版本化」的结论成立（修订历史不留存），但机制是「新计划新文件、修订覆
写当前文件」，不是简单的「总是覆写」。

### 1.3 截断界与截断语义

- `MaxPlanChars = 24000`、`MaxTaskChars = 8000`、`MaxReviewChars = 12000`
  （`pkg/turn/approval.go:977-981`）。评审提示词以
  `Truncate(req.Plan, MaxPlanChars)` 装载计划（`pkg/turn/approval.go:1040`）。
- `turn.Truncate`（`pkg/turn/approval.go:1160-1173`）：先 TrimSpace，按
  **rune** 数截到 max，截断时追加 `\n[truncated]` 标记。
- 真实体量（`~/.forebrain/workspace/plans/<本项目>/` 抽样）：典型计划
  23–31KB，最大 74.5KB（`mcp-startup-nonblocking-implementation.md`）。即
  24K rune 上界在最大的一批计划上**确实会截断**——diff 两侧同界处理（§4.4）
  不是理论洁癖。

### 1.4 diff 能力盘点（Q3 的决定性证据）

| 事实 | 证据 |
| --- | --- |
| Go 侧已有 diff 依赖：`github.com/pmezard/go-difflib`（go.mod 直接依赖） | `go.mod:30` |
| 既有唯一 diff 封装：`event.Build(path, before, after)` → unified diff（context=3）+ added/deleted 计数 | `pkg/event/turndiff.go:9-39` |
| **TUI 审批浮层已在用它渲染 edit/write 审批的 diff** | `pkg/tui/overlays.go:1677,1685`；无 `diff --git` 头的着色处理 `pkg/tui/render.go:6671`、`pkg/tui/overlays.go:1715-1724` |
| **web 已在渲染引擎产的 unified diff**（edit_file/write_file 卡片的 turnDiff） | `frontend/src/views/ChatView.vue:1304-1322`（`turnDiff.unifiedDiff`，注释「Shown whole: the preview scrolls」） |
| frontend **没有任何 diff 库** | `frontend/package.json` dependencies 全列（axios/camelcase-keys/clsx/katex/lucide/nanoid/snakecase-keys/tailwind-merge/vue/vue-router/vue-stream-markdown） |
| 引擎侧统一组装点：`(*PendingApprovalGate).Pending` 已在此填 PlanFilePath/PlanReviewModels/PlanReviews，两端共用 | `pkg/turn/approval.go:1604`、`:1673-1688` |

### 1.5 两端审批卡现状

- TUI：浮层 `approvalPlanContent` 读计划文件全文（`pkg/tui/overlays.go:1758-1768`），
  浮层内滚动；评审意见以 `req.PlanReviews []PlanReviewNote` 渲染在选项上方
  （`pkg/turn/model.go:564`，`planReviewNotesFrom` 投影 `pkg/turn/approval.go:1270-1286`）。
- Web：GET `/api/chat/sessions/{id}/approval-request`（`pkg/gateway/api_extra.go:2690-2725`）
  → `sessionApprovalRequestWire{plan_text, plan_review_models, plan_reviews,
  plan_review_active}`（`:2729-2737`）；`plan_text` 由 handler 读计划文件填充
  （`:2692-2697`）。前端类型 `api.ts:822-833`（`planText?/planReviews?`），
  渲染在 `PendingActionsPanel.vue:26-43`：计划正文 = `max-h-56 overflow-y-auto`
  滚动盒 + markdown（MessageResponse），评审意见卡其下。i18n 机制既有
  （`frontend/src/locales/index.ts`，zh/en 同键）。

### 1.6 真机事件样本（步骤 3 的离线验证，全部 `-readonly`）

数据库面全量盘点（owner 指认 `~/.forebrain/state/` 后复查）：

| 位置 | 状态 | 结论 |
| --- | --- | --- |
| `~/.forebrain/state/forebrain.state.sqlite` | 活库（WAL 模式，4,146,229 事件 / 842 actions） | 唯一含评审数据的库，下述样本均出自此 |
| `~/.forebrain/workspace/state/forebrain.state.sqlite` | 0 字节空文件 | 无数据 |
| `/tmp/forebrain-prd/state/`（PLAN_REVIEW_DELIVERY 验收隔离 home，计划文档行 167） | 已删除 | **含回传标记的验收序列（场景 A–D）已不存在**，标记行多轮真机验证不可达 |
| `~/.forebrain_01/state/` | 目录不存在 | — |

主库两类实测序列：

**(a) 三个含评审事件的会话（单轮，回传特性落地前）**：

```
approval_requested → approval_resolved(decision="review_requested")
→ plan_review_started → plan_reviewed(outcome="done")
→ approval_resolved(decision="approved")
```

- 实测 decision 值域（评审会话内）：`approved` / `review_requested` /
  `answered`。**`review_requested` 是真实存在的 resolution 值**（用户选了
  「让模型 X 评审」时 surface 记的 display history，action 仍 pending）——
  Q2 规则的 `decision == "denied"` 过滤是载重墙，不能省。
- 全库无 `error='plan-review:delivered'` 的 action（三重核实：精确等值、
  `LIKE '%plan-review%'`、842 行 denied/cancelled error 全量直方图）。

**(b) 一个真实多轮序列（会话 `cli-14a2baf9`，评审跑了但被手动 deny 关闭）**：

```
approval_requested(A) → approval_resolved(A, review_requested)
→ [评审运行，结果当时存 surface 内存] 
→ approval_resolved(A, denied, reason="改动 2 删除")   ← 用户原话
→ approval_requested(A') → approval_resolved(A', approved)
```

三个设计级发现：

1. **规则过滤分支实测通过**：`denied ∧ reason=用户话语` 不匹配标记 →
   规则降级为无基线、不显示 diff——正是降级矩阵预期的行为（真实数据上
   验证，非推演）。
2. **事件 reason 与 action error 的分叉是实测事实**：该序列事件
   `reason="改动 2 删除"`（用户原话），而 action 行 error 存
   `ComposeDenyFeedback` 组合文本（"The user asked zhipuai /
   glm-5.3-flash to review…"）。Q2 规则读**事件** reason 是正确选择——
   两条通道语义不同，读 action error 反而会把组合文本当判据。
3. **存量「无评审事件」会话真实存在**：该会话 17,917 条事件全量留存
   （无清理路径，代码零 `DELETE FROM fb_session_events`）却零
   plan_review 事件——评审结果事件化晚于该会话（`chat_surface.go` 注释
   "this surface no longer keeps the result anywhere" 所述重构之前，结果
   存 surface 内存）。降级矩阵 #2（delivered action 无 started 快照/
   存量会话）因此是**可达路径**，不是不可达防御。

- 评审事件落点核实：`planReviewEventTarget`（`pkg/tui/chat_surface.go:1003-1015`）
  解析到 pending 审批的 session（= action 的 session）——评审事件与 Q2
  规则扫描的会话同域，无跨会话错配。
- **含标记的多轮回传序列在可读数据中不可达**（验收数据随 `/tmp/forebrain-prd`
  删除）——多轮正确性以代码不变量论证（§3.3），单轮序列与过滤分支已有
  真机证据。

## 2. Q1 裁决：方案 A — `plan_review_started` 载荷携带计划快照

**裁决：A。** 快照即评审者裁决的原文；事件即存储（跨进程/重启/双端天然可
用）；append-only 契约不破（新增 optional JSON 字段，事件体是 payload blob，
无 schema 迁移）。

否决：B（计划文件版本化——动 state schema，为单一显示特性引入存储生命周期，
重）；C（回传时刻另发快照事件——与 A 等值但多一个事件类型，劣）。

### 2.1 字段定义

```go
// pkg/event/run_events.go —— PlanReviewStartedPayload 增加：
type PlanReviewStartedPayload struct {
    ActionID string `json:"action_id"`
    ReviewID string `json:"review_id"`
    Provider string `json:"provider,omitempty"`
    Model    string `json:"model"`
    Label    string `json:"label,omitempty"`
    // PlanText is the plan as the reviewer judged it, bounded by
    // turn.Truncate(plan, MaxPlanChars) — the same bound the review prompt
    // loads the plan under. Display-only: it feeds the approval surfaces'
    // "changes since the review" diff and is never composed into any model
    // input (see the cache-prefix note in the delivery plan's behavior
    // matrix).
    PlanText string `json:"plan_text,omitempty"`
}
```

- 截断语义：`turn.Truncate(plan, MaxPlanChars)`（rune 上界 + `[truncated]`
  尾标，`pkg/turn/approval.go:1160-1173`），与评审提示词同界
  （`BuildPrompt`，`pkg/turn/approval.go:1040`）。空串省略（`omitempty`）。
- 体积论证：上界 24K rune ≈ 24–72KB UTF-8。同会话既有事件
  `plan_reviewed.Text` 在事件路径上**不设上界**（`finish` 直存
  `result.Text`，`pkg/turn/approval.go:1475-1486`；`MaxReviewChars` 只在
  模型输入组装 `ComposeDenyFeedback` 处施加，`:1072`）——有界快照严格
  小于一个已存在且无界的字段，事件体增大可接受。

### 2.2 发布次序调整（实施注意）

现状 `RunPlanReview` 先 publish started、后读计划（`pkg/turn/approval.go:1470`
→ `:1487`）。实施时把读文件挪到 publish **之前**，把快照放进载荷；读失败或
为空时**仍发 started（快照为空）**再 `finish(failed)`——保住
started/reviewed 严格配对的不变量（`ActivePlanReview` 的开合判定依赖它，
`pkg/turn/approval.go:1309-1345`），失败路径行为与今天完全一致。

### 2.3 display-only 红线（缓存前缀）

`PlanReviewStartedPayload` 的全部既有消费者：TUI 通知/命令
（`pkg/tui/notify.go:892`、`pkg/tui/commands.go:595,948`）与
`ActivePlanReview` 派生（`pkg/turn/approval.go:1333`）——都是显示/派生。
模型输入组装只消费 `plan_reviewed`（`PlanReviewsForAction` →
`ComposeDenyFeedback`/`ComposeReviewDeliveryGuidance`，`pkg/turn/approval.go:1231`、
`:1129-1139`）。新字段沿载荷走同一通道，**不新增任何到达模型输入的路径**；
实施计划的测试清单里应有一条锁死：快照字段不出现在任何 prompt 组装函数的
输入里。

### 2.4 兼容性

- 旧客户端：JSON 解码忽略未知字段；`RunEventSchemaVersion` 不动。
- 存量会话：started 事件无 `plan_text` → 解码得空串 → §3.2 规则自然降级为
  「不显示 diff」。无迁移。
- STOP 条件复查：本裁决不破坏 append-only、不引 schema 迁移、不触模型输入
  路径——两条 STOP 均未触发。

## 3. Q2 链接规则：新审批 → 基线快照

### 3.1 规则（最终表述）

> 对当前 pending 的 exit_plan 审批 A：在同会话的 `approval_resolved` 事件里
> 从新到旧找第一条满足 `action_kind ≈ exit_plan_mode ∧ decision == "denied" ∧
> trim(reason) == "plan-review:delivered" ∧ action_id != A` 的事件，其
> `action_id` 记为 B；再在 `plan_review_started` 事件里从新到旧找第一条
> `action_id == B ∧ trim(plan_text) != ""` 的事件，其 `plan_text` 即基线。

### 3.2 伪码（实现放 `pkg/turn`，`ActivePlanReview` 旁）

```go
// PlanChanges is the diff one re-prompted exit-plan approval shows: what
// changed in the plan since the review whose delivery closed the previous
// approval. Computed once where both surfaces build the request.
type PlanChanges struct {
    UnifiedDiff string // event.Build output, context=3
    Added, Deleted int
    // Which review's snapshot is the baseline, so the heading can name it
    // and a degraded/older baseline stays legible.
    ReviewID, Provider, Model, Label string
}

func PlanChangesForApproval(ctx context.Context, log PlanReviewLog,
    sessionID, actionID, currentPlan string) *PlanChanges {
    // 1) newest marker-delivered resolution, excluding A itself (A is
    //    pending; its own resolution cannot exist — the exclusion is the
    //    whitelist close for the collision edge, see §3.4).
    for i := newestFirst(listEvents(ctx, log, sessionID, "approval_resolved")) {
        var p event.ApprovalResolvedPayload
        if !decode(i.Payload, &p) { continue }
        if !eqFold(p.ActionKind, "exit_plan_mode") ||
            p.Decision != "denied" ||
            strings.TrimSpace(p.Reason) != turn.PlanReviewDeliveredReason ||
            strings.EqualFold(strings.TrimSpace(p.ActionID), actionID) { continue }
        // 2) newest started snapshot on that action with non-empty text
        for j := newestFirst(listEvents(ctx, log, sessionID, "plan_review_started")) {
            var s event.PlanReviewStartedPayload
            if !decode(j.Payload, &s) { continue }
            if !eqFold(s.ActionID, p.ActionID) || strings.TrimSpace(s.PlanText) == "" { continue }
            baseline := s.PlanText // already bounded at publish; Truncate is idempotent here
            cur := turn.Truncate(currentPlan, turn.MaxPlanChars) // same bound both sides
            if baseline == cur { return nil }
            sum, _ := event.Build("plan", []byte(baseline), []byte(cur))
            if sum.Added == 0 && sum.Deleted == 0 { return nil }
            return &PlanChanges{UnifiedDiff: sum.UnifiedDiff, Added: sum.Added,
                Deleted: sum.Deleted, ReviewID: s.ReviewID,
                Provider: s.Provider, Model: s.Model, Label: s.Label}
        }
        return nil // delivered action without a snapshot: legacy session → degrade
    }
    return nil // no delivered review in this conversation → manual flow
}
```

（查询用现成的 `ListSessionEventsOfType`，与 `ActivePlanReview` 同款两次列举，
`pkg/turn/approval.go:1315-1319`。）

### 3.3 多轮正确性与不变量论证

1. 回传即关闭：评审 done 后 `DeliverPlanReview` 把该 action deny 为标记
   （`pkg/turn/approval.go:166-179`），并发布唯一一条
   `approval_resolved(denied, marker)`（TUI `chat_surface.go:957-967` / gateway
   `api_extra.go:2895-2905`，同一 action 一条）。
2. 修订在关闭之后：回传 resume 的指令头要求 planner「update the plan file,
   then call exit_plan_mode again」（`reviewDeliveryGuidanceHeader`，
   `pkg/turn/approval.go:1125-1127`）——新审批 A 的 `approval_requested`
   必然晚于 B 的 marker resolution。
3. 因此「最近的 marker resolution」在任意时刻无歧义地命名唯一基线 action。
   多轮（评审→回传→修订→再评审→再回传）下每轮各产生一条 marker
   resolution，规则取最近一条 = 最近一次被评审的版本。
4. 时序假设 A1（显式声明）：同会话同时至多一个 pending exit_plan（planning
   run parked、会话内 turn 串行）。若未来打破，规则仍取「最近 marker
   resolution」，唯一性不破；可选加固见 §3.4。

真机验证情况（步骤 3）：§1.6——单轮序列、decision 值域与 #4b 过滤分支已
实测；含 marker 的多轮序列随验收隔离 home 删除而不可达，正确性靠上述不变
量。

#4b 的可选扩展（本设计**不采纳**，记录备查）：把锚定从「marker
resolution」放宽为「任意 `denied ∧ 该 action 名下有 outcome=done 的
plan_reviewed` 的 resolution」，可让被用户抢答的评审也当基线。否决理由：
引入第二套锚定判据、且会命中 §1.6 (b) 所示前事件化时代的存量会话（无
plan_reviewed 事件可查），语义收益（抢答窗口极窄）不抵复杂度——白名单
收尾，不取。

### 3.4 歧义与降级矩阵（白名单收尾：绝不错基线）

| # | 场景 | 行为 | 理由 |
| --- | --- | --- | --- |
| 1 | 手动流/首版：会话无 marker resolution | 不显示 diff 段 | 无基线；渲染层唯一分支是「有 PlanChanges 才渲染」 |
| 2 | marker resolution 的 action 无 started 快照（存量会话、发布时读文件失败） | 不显示 | 伪码内层循环落空 → return nil |
| 3 | 基线 == 当前（同界比较相等，或 diff 无 +/- 行） | 不显示 | 伪码显式判等 |
| 4 | 回传后、新审批前，夹了一次用户手动 deny（用户话语，reason ≠ 标记） | **仍显示**，基线 = 最近被评审版本 | baseline 身份并不歧义：手动 deny 不产生 marker resolution，「最近一次评审裁决的版本」仍唯一；标题「Changes since the review」语义真实（把响应手动反馈的修改也如实显示）。计划文本预告过此歧义，裁决为不降级 |
| 4b | 评审已跑但被用户手动 deny 关闭（无任何 marker resolution：回传被用户抢答，或存量前回传时代会话） | 不显示 diff | 真机实测序列（§1.6 (b)）：`denied ∧ reason=用户话语` 被过滤 → 无基线。与 #4 的区别在于没有先行 marker resolution 可锚定。可选扩展（见下）可覆盖，默认不取 |
| 5 | marker-collision 碰撞：用户把标记串逐字当拒绝理由手输（MARKER_COLLISION 修复前的事件层残留） | 该伪造 resolution 的 action 若有评审 → 基线仍真实；无评审 → 无快照 → 不显示 | 事件层 reason 是显示契约、无第二判据（该计划 §4 明确保留 `Reason: act.Error` 不加判据）。本规则只可能落到真基线或降级，不会错基线 |
| 6 | crash window：deny 已落库但 `approval_resolved` 未发出（publish 失败/进程死亡） | 规则绑到更早一次回传（基线更旧）或无基线 | 事件是 display history 非 action 权威（`approval_events.go:62-63`）。缓解已内建：`PlanChanges` 携带 `review_id/provider/model`，标题点名基线评审者，旧基线可读可核 |
| 7 | 用户手工编辑计划文件（审批 pending 期间） | 正常显示 diff | 快照=评审时原文，编辑如实呈现——这是显示增量，不是错基线（计划文本事实 5 的边角，接受） |

可选加固（实施计划自行取舍，本规则不依赖）：解析出 B 后对 action store 做
一次 `Get`，以谓词确认（MARKER_COLLISION 落地前 `error == 标记`；落地后
`turn.ActionIsReviewDelivered`）。它同时收窄 #5/#6，代价是每次审批渲染多一
次索引读。

### 3.5 与并行计划的交互（显式声明）

- MARKER_COLLISION：本规则读事件 `reason` 字段，**不依赖**其内部判据；该
  计划不改变事件形状，落地前后本规则行为一致。其双读数落地后，若实施采纳
  §3.4 的可选加固，判据用 `ActionIsReviewDelivered`（`error` = 标记 ∧
  `answer_json` 站），与用户手输标记串在结构上不可碰撞。
- APPROVE_RETRY_REVIEW_CANCEL（已落地）：approve 重试取消在途评审 →
  `approval_resolved(decision=approved)`，不匹配 `denied ∧ marker` 过滤。
- GATEWAY_DELIVERY_OBSERVABILITY（已落地）：回传失败日志只加观测，不改事件。

## 4. Q3 展示形态与两端 parity

### 4.1 裁决：引擎侧统一 diff（对计划初判的修订）

计划文本初判「下发原始两版正文、前端渲染 diff」。**盘点证据推翻该初判**，
裁决为：引擎侧用既有 `event.Build` 计算统一 diff，随审批请求一次性下发，两
端各自做薄渲染。理由：

1. `go-difflib` 已是直接依赖（`go.mod:30`），`event.Build`（`turndiff.go:16-39`）
   是仓库唯一 diff 实现，context=3 的 unified 格式两端都已在渲染：
   TUI 审批浮层（`overlays.go:1677,1685`）、web 工具卡
   （`ChatView.vue:1304-1322`）。
2. frontend **没有任何 diff 库**（`package.json` 全列）。「前端渲染 diff」
   意味着新写并永久维护一个 TS 行级 diff 算法，与 Go 侧实现保持语义 1:1——
   两个算法的永久对齐负担，违背「TUI/web 内容语义 1:1」的常设裁决的达成
   方式。引擎计算 = 单一算法 + 两端薄渲染，恰是仓库既有 parity 机制
   （edit 的 `turnDiff` 就是引擎算、两端渲染）。
3. 载荷更小：unified diff 只含变更区 + 上下文，通常远小于 24KB 基线全文。

TUI 侧无需引入任何 diff 计算；web 侧只需一个「unified diff 行 → +/- 着色
行」的展示组件（无算法）。

### 4.2 填充点与字段

- 引擎：`(*PendingApprovalGate).Pending` 的 exit_plan_mode 分支
  （`pkg/turn/approval.go:1680-1688`）在填 `PlanReviews` 处一并填
  `req.PlanChanges = PlanChangesForApproval(ctx, g.Runs, sessionID, actionID,
  currentPlan)`。`currentPlan` 由调用处读 `req.PlanFilePath`（同函数
  `:1673-1678` 已解析）。`ToolApprovalRequest` 增加显示专用字段
  `PlanChanges *PlanChanges`（`pkg/turn/model.go:527` 结构体）。
- Web wire：`sessionApprovalRequestWire`（`api_extra.go:2729-2737`）增加：

```go
PlanChanges *planChangesWire `json:"plan_changes,omitempty"`

type planChangesWire struct {
    Added       int    `json:"added,omitempty"`
    Deleted     int    `json:"deleted,omitempty"`
    UnifiedDiff string `json:"unified_diff"`           // 前端 camelCase 化为 unifiedDiff，
    ReviewID    string `json:"review_id,omitempty"`     // 与 ChatView 既有 turnDiff.unifiedDiff 同构
    Provider    string `json:"provider,omitempty"`
    Model       string `json:"model,omitempty"`
    Label       string `json:"label,omitempty"`
}
```

  前端类型 `api.ts:822-833` 处补 `planChanges?`。

### 4.3 展示形态

- **TUI**：审批浮层在计划正文上方加「Changes since the review (<provider /
  model>)」段，行级 +/- 着色复用现有 diff 行配色（克制纯色、禁粉、禁渐变；
  与 `overlays.go:1677` 的 edit 审批 diff 同一套）。文案英文，与浮层现状
  一致。长内容随浮层滚动，不截断。
- **Web**：`PendingActionsPanel.vue` 在 `planText` 盒（`:26-31` 的
  `max-h-56 overflow-y-auto` 滚动盒）上方加同款容器渲染 diff 行（+ 绿 /
  − 红，沿用既有 edit diff 预览的语义色）。locale 中英同键
  （`locales/index.ts`），如 `approval.planChanges.title` /
  `approval.planChanges.since`。
- **配置键**：本设计**不引入任何新配置键**（无表单交付负担）。若实施中
  确需行为开关，按常设裁决 TUI/web 表单同计划交付。
- replay/live 1:1：diff 在审批请求时计算，不落事件——与 `plan_text` 今天
  的生命周期一致（pending 时经 approval-request 呈现，决议后卡片变决议行，
  `ApprovalCard.vue` 只回放 Confirmation 行）。历史回放不显示已决议审批的
  计划正文/diff，两端一致。

### 4.4 截断交互（两侧同界）

基线发布时已过 `Truncate(plan, MaxPlanChars)`；当前文在计算前过同一
`Truncate`。两侧同界后：两侧都超界 → `[truncated]` 尾标行成为共同尾部
（不出现在 diff 里）；仅当前超界 → 尾部 `+[truncated]` 行是**真实增量**
（评审后计划变长）。§1.3 的真机体量（最大 74.5KB > 24K rune）说明这不是
不可达分支。

## 5. Q4 边界（白名单收尾，不写不可达分支）

| 边界 | 行为 | 落点 |
| --- | --- | --- |
| 无评审历史 / 无基线（手动流、存量会话） | 不显示段 | 引擎返回 nil；渲染层唯一分支「有才渲染」 |
| 基线与当前完全相同 | 不显示段 | 伪码判等 + added/deleted 全零判 |
| 超长计划 | diff 段与计划正文同款折叠/滚动 | TUI 浮层滚动 / web `max-h` 滚动盒 |
| 用户手工编辑计划文件 | 如实显示编辑增量 | §3.4 #7，接受 |

## 6. 实施拆解（预估：引擎 S / TUI M / web M）

1. **引擎（S）**：
   - `PlanReviewStartedPayload.PlanText` + `RunPlanReview` 读序调整（§2.2）；
   - `PlanChanges`/`PlanChangesForApproval`（§3.2）+ `Pending` 填充（§4.2）；
   - 单测：多轮序列正确性、§3.4 矩阵逐行、同界截断交互、快照不进 prompt
     组装（§2.3 锁死项）。
2. **TUI（M）**：浮层「Changes since the review」段渲染 + +/- 着色 + 滚动；
   `commands/notify` 侧对既有 started 载荷消费不受影响（新增字段忽略）。
3. **web（M）**：wire 字段 + `api.ts` 类型 + `PendingActionsPanel` diff 盒 +
   locale 两键 + 组件测试（`ApprovalCard.test.ts` 同款风格）。
4. 验收：真机场景 A 扩展——评审 → 回传 → 修订 → 新审批卡显示「Changes
   since the review」；对照无评审手动流不显示；web 与 TUI 同会话呈现一致。

依赖顺序：引擎先行（TUI/web 消费其字段）；无 schema 迁移、无配置键、无新
依赖。

## 7. 风险与红线复查

- 缓存前缀（产品最高优先级）：快照 display-only，消费者清单见 §2.3，无模型
  输入路径；两条 STOP 条件（append-only 破坏 / 快照进模型输入）均未触发。
- append-only：仅新增 optional 字段；`fb_session_events.payload_json` blob
  存储，无迁移、无 `RunEventSchemaVersion` 变更。
- 并发/多副本：基线只读事件日志（`ListSessionEventsOfType`），与
  `ActivePlanReview` 同款无共享内存的推导，集群语义一致。
