# 221 — cron 投递事实晚于 status 落库：等投递的测试必须等投递（`TestCronFireParkedOnApprovalSettlesWhenItsRunEnds`）

- **基线**：`9ea9f4a0`（dev 顶端）。
- **状态**：DONE（2026-11-04 执行；已 rebase 后 ff-only 合入 dev `7d505aa3`，worktree 与分支已清理）
- **优先级 / 工作量 / 风险**：P2 / S / LOW（只改 `pkg/gateway/run_control_test.go`）
- **依赖**：无（218 修的 job 行两段写是同族但不同字段；本计划独立）。
- **执行隔离**：worktree + `exec/221` 分支 + 一笔提交；合并在 owner 批准后 ff-only。

> **Drift check（先跑）**：`git diff --stat 9ea9f4a0 -- pkg/gateway/run_control_test.go` 应无输出（在干净检出上）；`grep -n 'func fireWaitsForStatus' pkg/gateway/run_control_test.go` 应命中 `:1796`。任一不符就 STOP。

## 0. 为什么做（已确定性复现，2026-11-04）

`Scheduler.settle`（`pkg/turn/scheduler.go:382-432`）按三段写落库，顺序是设计使然：

1. `FinishRun` —— 一次 CAS，把 run 记录置为终态并写 output（「谁先关谁投递」的 exactly-once 依据）；
2. `DeliverText` 把答案交给渠道，成功后 `MarkFireDelivered` 写 `delivered_to`（`pkg/state/cron.go:485`，另一条 UPDATE）；
3. `recordOutcome` 写 job 行自己的摘要。

`TestCronFireParkedOnApprovalSettlesWhenItsRunEnds` 用 `fireWaitsForStatus`（只等 `Status == OK`，即第 1 段）返回的记录，**紧接着**断言 `rec.DeliveredTo == "firetest"`（第 2 段的事实）。慢引擎/负载下第 1 段的提交与第 2 段的写入之间有真实窗口，断言读到空串。

**复现证据（本轮）**：

- 合体树（218+219-220）MySQL 全包跑 601s：`--- FAIL: TestCronFireParkedOnApprovalSettlesWhenItsRunEnds`。
- 判别性实验：给测试替身 `fireGatewayChannel.DeliverOutbound` 注入 200ms 延迟以撑开该窗口 → `-count=3` **3/3 确定性失败**，报错恰为 `expected "firetest" / actual ""`。撤掉注入后单独 `-count=20` 全过 —— 证明它是窗口竞态、不是逻辑错误。

**为什么修测试而不是产品**：`delivered_to` 只能在投递**之后**写，而 `FinishRun` 的 CAS 必须在投递**之前**（否则两个 closer 会各投一次）。中间夹着外部副作用（网络投递），这两条 UPDATE 无法合成一个事务，所以「status 先落、delivery 后落」是正确设计；缺陷在测试把第 1 段当作「已 settle」。

## 1. 范围

**只动**：`pkg/gateway/run_control_test.go`。
**不动**：`scheduler.go`/`cron.go` 的写序（见 §0）；其它测试。

## 2. 修法

把「等状态」升级为「等事实」：

1. 新增 `fireWaitsFor(t, g, jobID, done func(state.CronRun) bool) state.CronRun`——轮询谓词由调用方给，注释写明 settle 的三段写序（status/output → 投递与 `delivered_to` → job 行摘要），以及「想要后一段的事实就必须等那一段」。
2. `fireWaitsForStatus` 变成它的瘦包装（`done = status 相等`），行为不变；调用点零改动。
3. 给 `fireWaitsForStatus` 的注释补一句它只等到第 1 段。
4. `TestCronFireParkedOnApprovalSettlesWhenItsRunEnds` 改用 `fireWaitsFor`，谓词为 `Status == OK && DeliveredTo == "firetest"`；其后的 `Output` 与 `out.delivered()` 断言随之成立（`MarkFireDelivered` 在 `DeliverText` 之后）。

## 3. 验证

1. **判别性**：临时给 `fireGatewayChannel.DeliverOutbound` 加 200ms 延迟（复现探针），`go test -tags fts5 ./pkg/gateway -count=3 -run 'TestCronFireParkedOnApprovalSettlesWhenItsRunEnds'` → 改前 3/3 红（`DeliveredTo` 空），改后 3/3 绿。验完删掉探针（`git diff` 不得留下它）。
2. 三库各跑该测试 `-count=5`（'' / mysql / postgres URL）→ ok。
3. MySQL 腿 `./pkg/gateway` 全包 → 绿（与 218、219-220 合体后应无其它失败）。
4. SQLite 全量 `./... -count=1 -p 2` → 全绿；`go vet -tags fts5 ./pkg/...` → 无输出。

## 4. 完成判据

§3 全绿；`git diff` 仅 `pkg/gateway/run_control_test.go`；无新增 `t.Skip`、无 sleep/重试式绕过（探针必须已删除）。

## 5. STOP 条件

- Drift check 不符。
- 改后探针仍在却仍红（说明还有别的写序假设没找到——停下重新归因）。

## 6. 维护注意

- 本仓已有两族「多段写」的测试约定：**断言哪一段的事实，就等哪一段**（218 覆盖 job 行 `LastStatus`，221 覆盖 `delivered_to`）。新写 cron/审批类测试时不要再出现「等 status 然后立刻读后写字段」。
