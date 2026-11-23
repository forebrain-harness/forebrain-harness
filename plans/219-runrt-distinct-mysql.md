# 219 — `waitRecoveryScan` 的 DISTINCT 剥离扩到 MySQL（213 的 PG 修复漏了 MySQL 臂）

- **基线**：`9ea9f4a0`（dev 顶端；217 已并入）。
- **状态**：DONE（2026-11-04 执行；已 rebase 后 ff-only 合入 dev `7d505aa3`，worktree 与分支已清理）
- **优先级 / 工作量 / 风险**：P1 / S / LOW（一个条件 + 注释；现成回归测试）
- **依赖**：无（218 发现转出；owner 裁定「本期必须修复」）。
- **执行隔离**：worktree + `exec/219-220` 分支 + 增量提交；合并在 owner 批准后 ff-only。

> **Drift check（先跑）**：`git diff --stat 9ea9f4a0 -- pkg/state/runrt.go pkg/state/runrt_test.go` 应无输出（在干净检出上）；`sed -n '1219p' pkg/state/runrt.go` 应为 `	if d == dbx.Postgres {`。任一不符就 STOP。

## 0. 为什么做（均已实测核实，2026-11-04）

213 的 `5ce6f559` 修过同一个缺陷的 PG 面：`ListResolvedWaitActionIDs` 的扫描（`pkg/state/runrt.go:1275` 传入 `DISTINCT w.action_id, …`）带 `ORDER BY w.updated_at`，而选择列表不含它——PG 报 `SQLSTATE 42P10`。修法是在 `waitRecoveryScan`（`runrt.go:1208`）里**只为 Postgres 剥掉 DISTINCT**（注释论证：`fb_run_waits.action_id` 是 UNIQUE、join 走主键，一行至多出现一次，DISTINCT 本就无效果）。

**MySQL 8 有同一条规则**（`Error 3065 … incompatible with DISTINCT`），剥 DISTINCT 的条件却没带上它。后果（未修复 dev `9ea9f4a0` 实测）：

- `pkg/state` 对 MySQL 全包**只有这一个失败**：`TestRecoveryScanRunsOnEveryDialect`（213 自己留下的三库回归测试）——`ListResolvedWaitActionIDs(""): Error 3065`。
- `pkg/gateway` 对 MySQL 全包也只有它带的失败：`TestTheReaperLeavesAnUncertainContinuationToItsRecovery`（恢复扫描失败 → `recoverResolvedApprovalWaitsOnce` 提前 return → run 停在 "running"；执行者 -count=10 隔离 10/10 确定性复现，评审全包复跑 1/1）。
- **真实部署影响**：MySQL 网关每次启动/巡检的审批恢复扫描全部失败，被中断的审批运行永远不会恢复。

## 1. 范围

**只动**：`pkg/state/runrt.go`（`waitRecoveryScan` 的方言条件 + 注释）。

**不动**：SQLite 语句文本（`TestSQLiteStatementsUnchanged` 快照守卫保护，一字不改）；其它 PG-only 分支（cron.go:438、rewind.go:501、runrt.go:800/1608、session_events.go:492、session_store.go:716、output_history.go:176/717 已逐一核实为合法引擎差异——RETURNING/LastInsertId、EXISTS 布尔、IFNULL/COALESCE、json_valid，无同型漏 MySQL）。

## 2. 修法

`runrt.go:1219`：

```go
// 改前
if d == dbx.Postgres {
	columns = strings.TrimPrefix(columns, "DISTINCT ")
}

// 改后
if d == dbx.Postgres || d == dbx.MySQL {
	columns = strings.TrimPrefix(columns, "DISTINCT ")
}
```

注释同步：把「PostgreSQL refuses …（SQLSTATE 42P10）」扩为同时引 MySQL 的同形拒绝（`Error 3065: Expression #1 of ORDER BY clause is not in SELECT list … incompatible with DISTINCT`），DISTINCT 无效果的论证原文保留。

## 3. 验证（每条带 `FOREBRAIN_HOME="$(mktemp -d)"` + `PKG_CONFIG_PATH` 指向本树 `third_party/pdfium/$(go env GOOS)_$(go env GOARCH)`；禁全局 CGO_LDFLAGS）

1. 回归测试翻绿：MySQL 腿 `go test -tags fts5 ./pkg/state -run 'TestRecoveryScanRunsOnEveryDialect' -count=1` → ok（改前红，改后绿——这就是「关掉修复即复现」的既有测试）。
2. SQLite 语句不变：`go test ./pkg/architecture/ -run 'TestSQLiteStatementsUnchanged' -count=1` → ok。
3. gateway 侧同愈：MySQL 腿 `go test -tags fts5 ./pkg/gateway -run 'TestTheReaperLeavesAnUncertainContinuationToItsRecovery' -count=10` → 10/10 ok。
4. `pkg/state` MySQL 全包 → ok；PG/SQLite 同测试 → ok。

## 4. 完成判据（机器可查）

§3 的 1–4 全绿；`git diff` 仅 `runrt.go` 一个文件。

## 5. STOP 条件

- Drift check 不符。
- MySQL 上 `pkg/state` 出现本计划之外的失败（应无——改前全包也只此一个）。

## 6. 维护注意

- 教训（213 遗留）：加了三库回归测试之后**必须真的把三库矩阵都跑一遍**——213 只验了 PG 腿，MySQL 腿一直红到今天。`scripts/test-db.sh mysql` 就是那条腿。
