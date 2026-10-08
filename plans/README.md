# plans/ — 审计产出的实施计划索引

来源：2026-10-08 branch 审计（范围=未提交工作区 diff；PLAN_REVIEW_DELIVERY 批次标
`introduced`，gateway status/stop 友好化批次标 `pre-existing`）。每份计划自包含，
执行者无需本会话上下文。

| 顺序 | 计划 | 来源发现 | 工作量 | 风险 | 标签 | 依赖 | 状态 |
|---|---|---|---|---|---|---|---|
| 1 | [GATEWAY_DELIVERY_OBSERVABILITY_PLAN.md](GATEWAY_DELIVERY_OBSERVABILITY_PLAN.md) | #1 | S | LOW | introduced | 无（建议在 PLAN_REVIEW_DELIVERY 之后合入） | DONE（2026-10-08 executed） |
| 2 | [APPROVE_RETRY_REVIEW_CANCEL_PLAN.md](APPROVE_RETRY_REVIEW_CANCEL_PLAN.md) | #4 | S | LOW | introduced | 无 | DONE（2026-10-08 executed） |
| 3 | [PLAN_REVIEW_MARKER_COLLISION_PLAN.md](PLAN_REVIEW_MARKER_COLLISION_PLAN.md) | #3 | M | MED | introduced | 无 | DONE（2026-10-08 executed） |
| 4 | [GATEWAY_DISPLAY_HOST_DEDUP_PLAN.md](GATEWAY_DISPLAY_HOST_DEDUP_PLAN.md) | #2+#5 | S | LOW | pre-existing | 无 | DONE（2026-10-08 executed） |
| 5 | [REVISED_PLAN_DIFF_VIEW_PLAN.md](REVISED_PLAN_DIFF_VIEW_PLAN.md) | D1 | M-L | LOW（spike） | direction | 建议 1-3 先合入（其行为基线更稳） | DONE（spike；交付物 [docs/plan/REVISED_PLAN_DIFF_DESIGN.md](../docs/plan/REVISED_PLAN_DIFF_DESIGN.md)，实施另立计划） |
| 6 | [GATEWAY_PROBE_TEST_PORT_RACE_PLAN.md](GATEWAY_PROBE_TEST_PORT_RACE_PLAN.md) | 二轮#1 | S | LOW | introduced（flake） | 无 | DONE（2026-10-08 executed，评审 APPROVE） |
| 7 | [PLAN_REVIEW_DELIVERY_STATUS_BACKFILL_PLAN.md](PLAN_REVIEW_DELIVERY_STATUS_BACKFILL_PLAN.md) | 二轮#2 | S | LOW | introduced（docs） | 无 | DONE（2026-10-08 executed，评审 APPROVE） |

排序原则：杠杆=影响÷工作量，置信度与修复风险折减。#1/#4 是刚落地特性的收尾
（S、LOW、验证路径干净）；#3 是正确性边缘但需要动刚建立的契约（M、MED）；
#2 是既有债务顺手清（S）；D1 是产品方向，先 spike 再定实施。

执行纪律（全计划通用）：
- 测试先行；`CGO_ENABLED=1 go test -tags fts5` 是唯一可信 Go 测试形态。
- 每份计划落地后：`gofmt -l pkg cmd` 为空、`go vet ./...` 0、
  `scripts/package-graph.sh` 无 diff、全量 `./...` 回归 ok。
- 改动留工作区，禁止 git commit（owner 手动审阅提交）。

## 执行记录（2026-10-08，improve execute）

五份计划全部执行完毕、评审 APPROVE，改动留工作区待 owner 审阅。执行编队：
独立计划并行派发 subagent（1→2 串行因同文件；2 评审后 3/4/5 三路并行），
每份由评审者亲跑验收命令。全局收口：`gofmt -l pkg cmd` 空、`go vet ./...` 0、
`scripts/package-graph.sh` 两次运行字节一致（仅 LOC 计数变化，零 import/边
变更）、全量 `CGO_ENABLED=1 go test -tags fts5 ./... -count=1` 34 包 ok。

- **1 GATEWAY_DELIVERY_OBSERVABILITY**：`deliverGatewayPlanReview` 区分
  `ErrNotPending`（静默）与其他错误（slog.Error）；`gatewayResumeState` 纯函数
  抽取 + `TestGatewayResumeStateCarriesDeliveredReview`。无偏差。
- **2 APPROVE_RETRY_REVIEW_CANCEL**：两 surface 的 Idempotent approved 分支补
  `cancelInFlightPlanReview`；TUI/gateway 各一条新测试（先落决策制造幂等态）。
  NOTES：`refreshSandboxRuntime` 在该分支被跳过经分析为非缺口（分支契约=同
  进程派发失败恢复，首响应已 refresh），未扩面。
- **3 PLAN_REVIEW_MARKER_COLLISION**：`DenyWithAnswer`（CAS）+ `ApprovalStore`
  扩展 + `DeliverPlanReview` 双读数 + `ActionIsReviewDelivered` 谓词三处消费；
  显示契约零变更（`Reason: act.Error` 不动）。存量无戳回传行按计划选 (a)
  降级（run 已终结不可 resume，路径不可达）。标记裸串非测试命中仅常量定义。
- **4 GATEWAY_DISPLAY_HOST_DEDUP**：`displayHost` 单一映射源、
  `gatewayProbeTimeout` 常量同源（文案派生）、`serve_run.go` 零改动；
  输出逐字不变由表测锁定（含 displayHost 一致性双断言）。
- **5 REVISED_PLAN_DIFF_VIEW（spike）**：交付
  `docs/plan/REVISED_PLAN_DIFF_DESIGN.md`（Q1-Q4 全裁决）。**有据修订**：Q3
  由计划初判「下发两版正文、前端渲染 diff」改为引擎侧 `event.Build` 统一
  diff（frontend 零 diff 库、两端已在渲染引擎产 diff、parity 单算法）；
  计划事实 3 精确化为「新计划新文件、修订覆写当前文件」。多轮正确性以
  不变量论证（可读数据中无含标记的多轮序列）。

## 二轮 branch 审计（2026-10-08 晚，范围=同一工作区 diff 的复核）

四包并行测试复现 gateway flake 一次（`refusedAddr` TOCTOU，见计划 6）；
`go build ./...`、`go vet ./...`、touched 包测试其余全绿。产出计划 6/7。

### 本轮考虑并否决（勿重审）

- **deny 不取消在途评审**：`docs/plan/PLAN_REVIEW_DELIVERY_PLAN.md:25` 已
  明确裁决——批准=取消；deny 走 `ComposeDenyFeedback` 既有通道。by-design。
- **`gatewayResumeState` 字段级复制的 lockstep 隐患**：注释自带警告 +
  `TestGatewayResumeStateCarriesDeliveredReview` 已钉住；跨包无编译期穷举
  手段，现状务实。
- **render.go 删 `truncateURLForHeader`/`middleTruncate`**：生产引用清零，
  头部走 `wrapToolDisplayLine` 折行，符合"长内容滚动不截断"既定产品裁决。
- **`RowsAffected` 后 `_ = err` 吞错**（`pkg/state/action_service.go`
  Approve/Deny/DenyWithAnswer 三处）：pre-existing 模式复制；sqlite 成功
  Exec 后 RowsAffected 出错实际不可达。顺手清理项，不独立成计划。
- **`displayHost` 签名 net.Addr→string**：banner 与 status 输出共用单一映射
  源，表测锁定输出逐字不变。by-design 去重。

## 执行记录（2026-10-08 晚，二轮 improve execute：计划 6/7）

两计划文件不相交，按并行规则同时派发两执行者（主工作树直改——目标代码
只存在于未提交工作树，从 HEAD 拉隔离 worktree 会缺失全部改动对象；沿上轮
先例，零 commit 留工作区待 owner 审阅）。评审=重跑全部 done criteria +
逐 hunk 读 diff + 事实抽查，双双 APPROVE。

- **6 GATEWAY_PROBE_TEST_PORT_RACE**：`refusedAddr` 重写为拨号验证
  ECONNREFUSED 才返回（8 次换候选，抢端口场景静默换下一个）。执行者增量
  恰 +19 行（函数体+注释+`time` 导入），两个 NotRunning 测试断言未动，
  `errors`/`syscall` 已有导入复用。评审亲验：gofmt 空、vet 0、定向测试绿、
  并行拓扑（turn/gateway/run/state）复跑全 ok。有据解读：done criteria
  "pkg/gateway 下仅一个 M" 在故意 dirty 的树上按"本人增量仅此一文件"执行。
- **7 PLAN_REVIEW_DELIVERY_STATUS_BACKFILL**：`docs/plan/` 计划文档末尾追加
  Implementation Status（:219-261，落地内容表格/偏差/验证/未做），明确记录
  "字符串白名单 → marker+answer-stamp 双读数"取代及理由（防用户手打 marker
  误判）。执行者对全部 file:line 亲 grep 后书写，并把计划笔误"三处消费"按
  实证改为两处直接+一处经 `gatewayResumeState` 间接；插入位置一次中部事故
  经 verify 当场暴露并自愈（章节结构评审复核完整）。评审抽查 8 处 file:line
  引用全部精确命中。
