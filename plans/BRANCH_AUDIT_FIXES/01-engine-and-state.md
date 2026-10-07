# 01 引擎与状态：死锁、保留期 TOCTOU、收割盲区、租约清理、心跳不续

来源发现：I-2、I-4、I-5、I-9（introduced）；owner 裁决 E1（心跳不续）。E2 原定的升级文档已被 owner 撤销——本计划不碰任何文档文件。
涉及包：`pkg/process`、`pkg/turn`、`pkg/state`、`pkg/gateway`（A5 一处）。
全部改动写进已有文件，不新建任何 `.go` 文件。

## 前置检查（漂移核对，逐条过了再动手）

1. `pkg/process/cron_service.go`：`Bind` 在 `c.mu` 临界区内调 `c.stop()`（约 78-83 行）；`Stop` 同型（约 105-117 行）；`Maintain` 取同一把 `c.mu`（约 216-226 行）；`SettleFire`/`FireParked` 已经是"锁内拷指针、锁外调用"的写法（约 171-196 行）。
2. `pkg/turn/scheduler.go`：`Start` 返回的 stop 是 `cancel(); <-done`（约 161-164 行）；`RunDue` 在 tick 协程内同步调 `Deps.Maintain`（约 170-177 行）；`runDueJobs` 用 `s.claim` + `Store.ClaimDueJob`（DB CAS）认领后才 `go s.fire(...)`（约 200-232 行）——即 fire 本就在独立协程，`stop()` 只等 tick 循环退出。
3. `pkg/state/session_store.go`：`DeleteSessions` 事务内对 `fb_sessions` 的 DELETE 无任何存活复查（约 354-405 行）；`StampRunTiming` 先写 `started_at_ms/finished_at_ms`（约 1363-1374 行）。
4. `pkg/state/runrt.go`：候选查询只看 `status=running` + owner 存活（约 1136-1144 行），CAS 带 `AND finished_at_ms IS NULL`（约 1179 行）；`livePrimaryRunCondition` 常量（约 206 行）与 `liveRunArgs` 存在；全仓唯一 `DELETE FROM fb_run_owners` 是自愿注销（约 1105 行）。
5. `pkg/gateway/run_control.go`：`startHeartbeatTurn` 用 `Origin{Surface: turn.SurfaceWebChat, ChannelID: "heartbeat"}`、`Trigger: heartbeatTrigger`（约 790-805 行）；`startCronFire` 用 `Surface: turn.SurfaceChannel`（约 838-847 行）；`detachedTurn` 结构体与 `startDetachedTurn`（约 600-648 行）；`turnEnded`/`accepts` 在 `pkg/turn/submit.go`（约 328-460 行）。

## A1 [CORRECTNESS] 修掉 CronService.Bind/Stop 持锁等待旧调度器的死锁（I-2）

**根因**：`Bind`/`Stop` 违反了本文件自己 `SettleFire`/`FireParked` 已示范的规矩——持 `c.mu` 期间不得等待任何可能反过来需要 `c.mu` 的协程。`stop()` 内 `<-done` 等 tick 协程，tick 协程正卡在 `Maintain` 抢 `c.mu`：循环等待，Bind 的 HTTP 请求与整个 cron 机制永久挂死。切换代理（`agents_api.go:158`、`server.go:593`）或关停撞上 30 秒 tick 即触发。

**改动**（`pkg/process/cron_service.go`，只动 `Bind` 与 `Stop`）：

- `Bind`：锁内完成全部状态安装（旧 stop 拷出并置 nil、装新 scheduler、设 agentID、清 lastSweep、`c.stop = next.Start(ctx)`），**释放锁之后**再调旧 stop：
```go
c.mu.Lock()
old := c.stop
c.stop = nil
// …安装 next（现 84-100 行的逻辑原样）…
c.mu.Unlock()
if old != nil {
    old() // 旧 tick 协程可能正等 c.mu；等它退出绝不发生在持锁期间
}
```
- `Stop`：同型——锁内拷出 `old := c.stop` 并清 `c.stop`/`c.sched`/`c.agentID`，解锁后 `old()`。
- 把 `SettleFire`/`FireParked` 上方那句范式注释提炼成一条普适注释放在 `Bind` 上方：**持 `c.mu` 期间永不等待任何会取 `c.mu` 的协程；一切等待都在锁外。**

**为什么这样是根因修而不是补丁**：它消灭的是"持锁等待"这个类，而不是只给 `Maintain` 这一个取锁点拆锁。`s.fire` 已是独立协程、fire 认领是 DB CAS（`ClaimDueJob`），所以"锁外等旧协程"不会让同一触发被点两次，也不会让旧代理的任务在切换后新开 fire——旧协程只是把已越过认领点的当前这一轮跑完，与今天 `stop()` 语义除竞态窗口外完全一致。

**缓存影响**：无。不触碰任何提示组装路径。

**测试**（`pkg/process/cron_service_test.go`，白盒，同包）：复刻死锁形状的确定性回归——
1. 用临时 sqlite 建 `CronService` 并 `Bind`（Deps 的 `Maintain` 换成测试桩：先 `c.mu.Lock(); c.mu.Unlock()`（复刻真 `Maintain` 的取锁），再等一个 gate channel）。
2. 让一次 pass 进入桩 Maintain（gate 未关），另起协程调 `Bind`（换一个 agentID）。
3. 关 gate；断言 `Bind` 在超时内返回、新 agentID 生效。修复前此测试死锁（靠 `go test` 超时暴露），修复后通过。`Stop` 同理补一条。

## A2 [CORRECTNESS] 给保留期删除补上事务内的存活复查（I-4）

**根因**：`ExpiredFires` 在查询时判活（`pkg/state/cron.go:436-455`），`DeleteSessions` 拿到列表后才在另一个事务里删（`cron_service.go:270`），事务内不复查。用户恢复过期 cron 对话并发消息（`run_control.go` 按 session 打开旧 cron 会话是允许路径）撞上清扫 → 会话连消息级联删除、空壳复原。

**改动**（`pkg/state/session_store.go` 的 `DeleteSessions`）：

- 每个 chunk 的 `DELETE FROM fb_sessions` 加存活否决，复用同包常量（占位符实参顺序照 `cron.go:440-452` 的用法对齐）：
```sql
DELETE FROM fb_sessions WHERE agent_id = ? AND id IN (...)
  AND NOT EXISTS (
    SELECT 1 FROM fb_runs rr
    WHERE rr.session_id = fb_sessions.id AND rr.parent_run_id IS NULL
      AND <fmt.Sprintf(livePrimaryRunCondition, "rr") + liveRunArgs(time.Now())>)
```
- `SessionLeftovers` 增加一个字段（如 `Survived []string`）回报"因存活运行被跳过"的会话 id；调用方 `pruneExpiredFires` 对这些会话的 fire 本轮不动（顺延到下轮清扫）。
- 同步更新 `DeleteSessions` 的 doc 注释："另一个代理拥有的会话不碰"之后补"有活着的主运行的会话本轮不碰"。

**缓存影响**：无。

**测试**（`pkg/state/session_store_test.go`）：(1) 目标会话有一条 running 主运行 → 删除调用后会话仍在、`Survived` 含它；(2) 无运行 → 照删；(3) 运行已终态 → 照删。`pkg/process/cron_service_test.go` 补一条：清扫撞上存活会话时 fire 记录不被结算、下轮可清。

## A3 [CORRECTNESS] 让收割器收走"时钟已落、状态未落"的 running 僵尸行（I-5）

**根因**：`StampRunTiming` 先落 `started_at_ms/finished_at_ms`（状态仍 running），进程随即死在写终态之前；收割 CAS 的 `AND finished_at_ms IS NULL`（`runrt.go:1179`）永远匹配不上这类行——D8 要根除的"永远执行中"以小号复活，且若是 cron 首运行，执行记录永不结算。

**改动**（`pkg/state/runrt.go` 的 `ReapAbandonedRuns`）：把现有 CAS 改成一条保留已落时钟的语句（UPDATE 表达式按 SQL 语义读旧值，与现有注释 1164-1168 一致）：

```sql
UPDATE fb_runs
SET status=?, updated_at=?,
    started_at_ms=COALESCE(started_at_ms, created_at*1000),
    finished_at_ms=COALESCE(finished_at_ms, <clockExpr>),
    worked_ms=COALESCE(worked_ms, <clockExpr>-created_at*1000)
WHERE id=? AND status=? AND owner=? AND (finished_at_ms IS NULL OR finished_at_ms IS NOT NULL)
RETURNING finished_at_ms
```

（等价地也可保留两条 CAS：带 `IS NULL` 的原语句 + 只改 status 的 `IS NOT NULL` 语句。二选一，倾向单语句。）结束事件的发布与 fire 结算路径完全复用现有 `out` 分支，不动。owner 存活条件维持候选查询原有的过滤，不放宽。

**缓存影响**：无。

**测试**（`pkg/state/runrt_test.go`）："先 Stamp 后崩溃"定点用例——插入 running 行 → `StampRunTiming` → 无 owner → `ReapAbandonedRuns` 后：status=failed、已盖的 started/finished/worked 原样保留、结束事件照常可读（`ListRunEventsOfTypes` 有 TurnError/TurnCancelled 语义的事件，按现有实现的事件类型断言）。补一条对照：未 Stamp 的行走原路径，时钟表达式结果与现在一致。

## A4 [TECH DEBT] 清理 fb_run_owners 里死进程遗留的租约行（I-9）

**改动**（`pkg/state/runrt.go` 的 `ReapAbandonedRuns`）：同一事务内、提交前顺带
`DELETE FROM fb_run_owners WHERE heartbeat_at_ms < ?`（实参复用 `staleBefore`）。过期心跳读作非存活的既有语义不变，这只是把"读作死"的行真正删掉。

**缓存影响**：无。

**测试**（`pkg/state/runrt_test.go`）：过期 owner 行被删；新鲜 owner 行保留；自愿注销路径（1105 行）不回归。

## A5 [CORRECTNESS] 心跳回合撞用量上限不自动继续（E1 裁决）

**根因**：心跳回合 `Origin.Surface=webchat`（标签与流式路径依赖它，不能改 surface），于是落进 auto-continue 的 surface 白名单。cron 触发用 channel surface 表达"没人看着"（D7）；心跳的同一语义没有表达渠道。

**改动**（三处，全部已有文件）：

1. `pkg/turn/model.go`：`TurnRequest` 增加导出布尔字段 `Unattended`（注释写明语义：*"An unattended turn has nobody watching it: a usage limit ends it for good — the next trigger comes on schedule or by a person, never by a continuation timer."*）。零值不变，现有构造点全部不受影响。
2. `pkg/turn/submit.go` 的 `autoContinuer.turnEnded`：usage-limit 分支的武装点加守卫——`req.Unattended` 时不安排 continuation（既不武装、也不动 `fired` 计数的既有簿记；心跳提交时 `turnStarting` 已把该会话可能挂着的 continuation 按既有逻辑作废）。其余结尾路径（非 usage limit 的收尾）保持现状。
3. `pkg/gateway/run_control.go`：`detachedTurn` 增加 `Unattended bool`；`startDetachedTurn` 透传进 `TurnRequest`；`startHeartbeatTurn` 置 `Unattended: true`。`startCronFire` 不动（D7 机制维持）。`continueAfterUsageLimit` 不置位（续跑回合本身必须可再续）。

**缓存影响**：无。旗标只影响回合结束后的调度决策，不进任何提示字节。

**测试**：
- `pkg/turn`（submit/session 的既有测试文件）：unattended 回合 + usage limit → `PendingAutoContinuePlan` 为空、无 timer；对照 webchat 非 attended 回合照常武装。再补一条：unattended 非 usage-limit 结尾的簿记不回归。
- `pkg/gateway`（`run_control_test.go`）：心跳触发的 detached turn 携带 `Unattended`（按该文件现成的 fake Core 断言 TurnRequest 字段）。
- 真机：usage-limit 场景难以按需复现，按全局规则 8 记"场景难复现，以单测为准"；心跳本身的真机路径（SRT-001 既有验收）不回归即可。

## A6 [CORRECTNESS] 执行期发现的既有缺陷，并入本期（owner 规矩：发现的缺陷必须修）

**A6a rows.Err() 同族缺失（生产 3 处 + 测试 5 处）**：`for rows.Next()` 循环结束后不查 `rows.Err()`，迭代中途出错被静默吞掉——

- `pkg/state/session_store.go`（ForkSession 消息复制循环，约 2109 行）：迭代错误 → `copied` 静默截断 → **不完整的 fork 被当成功提交**。修法：循环后、`rows.Close()` 前查 `rows.Err()`，非 nil 即返回该错误（事务回滚）。
- `pkg/migrate/codex.go:289`（threads 索引读取）与 `:373`（stage1_outputs 读取）：迭代错误 → 部分数据静默缺失、迁移报成功。修法同型：循环后查 `rows.Err()`，按各自函数的错误/Degraded 既有口径返回。
- 测试同型 5 处一并修：`pkg/state/schema_migrations_test.go`（约 686、757、919 行）、`pkg/state/session_store_test.go:202`、`pkg/tool/request_permissions_test.go:3296`。

**A6b `TestCreateRunIsAtomicAcrossStores`（pkg/state/runrt_test.go:393）饿死型 flake**：`both owners must win sometimes, got map[b:50]`——被测属性（maxLive==1）成立，但"双方都必须赢"是把 goroutine 调度公平当前提，紧旋重试下 sqlite 锁可让一方连赢 50 次（本次执行期在本机复现 2/5 轮）。与 01 的改动无关（CreateRun/SetStatus 路径未触碰），属既有测试缺陷。修法（改测试前提，不动生产码）：失败/成功两侧都加让步（忙等侧 `runtime.Gosched()` 或 1ms 级 sleep，成功侧 SetStatus 后短暂让窗），使公平前提可被调度满足；顺带把 `countLive` 里非测试 goroutine 调 `t.Fatal` 的误用改为 `t.Errorf` 口径（t.Fatal 只许在测试 goroutine 里调）。

**缓存影响**：无。

**测试**：A6a 的 ForkSession 用例——注入迭代错误不可行则以下一条为准：现有 fork 测试全绿（回归）；migrate 侧现有测试全绿。A6b：连跑 `go test -run TestCreateRunIsAtomicAcrossStores -count=20` 稳定通过。

## 验证（本计划全部完成后）

```bash
gofmt -l pkg cmd
go vet -tags fts5 ./...
CGO_ENABLED=1 go test -tags fts5 ./pkg/process/ ./pkg/turn/ ./pkg/state/ ./pkg/gateway/ -count=1
```

死锁回归测试修复前先跑一次确认它确实挂（可选，用 `go test -timeout 30s` 观察），修复后转绿。
