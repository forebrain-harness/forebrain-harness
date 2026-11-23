# Plan 048: v10→v11 的流式增量清理按索引与单调游标推进——大库迁移从数小时降到数分钟

> **SUPERSEDED（2026-10-14 复评，不要执行）**：owner 的库自己跑完了 v10→v14——v11 汇总耗时 93 分 32 秒，且 `main_agent_rows_deleted=0`。本计划的量级估算被实测推翻：主清理在这台库上一次扫描就返回（「平方级」只在主代理解析出过海量增量的库上成立，而这台库 7.5M 增量全来自 subagent）；工具输出阶段在 subagent 阶段之后运行，当时事件表已缩到可忽略，逐 step 全表扫描是零头。对本用户而言这是一次性迁移且已完成，改删数据路径的收益不抵风险。**唯一保留的部分已拆为 `plans/052-v11-migration-progress-logs.md`**（93 分钟零输出正是这次误判为「卡死」的原因）。理由与数据见 `plans/README.md` 顶部复评。】

> **Executor instructions**: Follow this plan step by step. Run every
> verification command and confirm the expected result before moving to the
> next step. If anything in the "STOP conditions" section occurs, stop and
> report — do not improvise. When done, update the status row for this plan
> in `plans/README.md`.
>
> 本计划只做「让 v11 清理变快、且过程可见」，**不改清理的语义**：删哪些行、合并成什么形状、计数怎么报，必须与今天逐字一致。
> 现有测试 `TestMigrateAwayStoredStreamDeltas` 是这条不变量的守卫；除计划明确点名的两处（fixture 增补 + 批大小注入）外，不许改它的断言。
>
> **Drift check (run first)**: `git diff --stat a8140fc..HEAD -- pkg/state/schema_migrations.go pkg/state/schema_migrations_test.go pkg/state/schema.sql`
> 若这三个文件在 a8140fc 之后有改动，先把 "Current state" 的摘录与实码逐段对照；对不上按 STOP 处理。

## Status

- **Priority**: **P1**（一个真实用户库的启动已因此卡死 1.5 小时以上，且按当前代码推算第三阶段在同类库上不可能跑完）
- **Effort**: M（约 120 行含注释与测试）
- **Risk**: MED（改的是删用户数据的迁移路径；缓解：清理集合与结果形状逐字不变，且新增两类测试——多批次端到端 + 查询计划断言）
- **Depends on**: none（049、050 同属迁移管线，可与本计划并行或随后；三者的测试注入变量互不重叠）
- **Category**: perf / migration
- **Planned at**: commit `a8140fc`, 2026-10-14

## Why this matters

真实库实测（owner 机器，`~/.forebrain/state/forebrain.state.sqlite`，5.6 GB，`fb_session_events` 7,691,906 行）：

| event_type | 行数 |
|---|---|
| reasoning_delta | 6,981,921 |
| assistant_delta | 551,353 |
| tool_output_delta | 25,167 |
| reasoning_done | 14,143 |

即 **98% 的事件行是 v11 要清理的流式增量**。该库上这三条语句的计划（`EXPLAIN QUERY PLAN`，基于本仓库 `schema.sql` 实测）：

```
-- deleteMainAgentStreamDeltasV11 的内层 SELECT
SCAN fb_session_events
-- pruneToolOutputDeltasV11 的每个 tool step：COUNT / DELETE / merge 读取
SCAN fb_session_events
```

后果按阶段：

1. **主代理清理是平方级的。** `DELETE ... WHERE sequence IN (SELECT ... ORDER BY sequence LIMIT 50000)` 每批都从表头重新扫，删除进度越深、每次要越过的前缀越长。D 条匹配行的总行访问量约为 `D²/(2×50000×p)`；这里的 D 是百万级。
2. **工具输出清理是「每 step 一次全表扫描」。** `COALESCE(run_id,'') = ?` 让 `idx_fb_session_events_run_type` 完全失效（表达式作用于被索引列），而没有任何索引以 `event_type` 打头，所以每个 step 的 COUNT 与 DELETE 各扫一遍 7.7M 行、每行还带 `json_extract`。本库有 42,094 条 `tool_call_started`、25,167 条 tool 输出块；上万 step × 每步数秒到数十秒 = 天数级。**这是本迁移在同类库上永远跑不完的原因，不是 VACUUM。**
3. **过程零输出。** 13:31:43 打出「backed up the state database before migrating」之后，直到迁移结束都不会再有一行日志，`migrateStateSchema` 里三处子步骤也没有进度行。

一项被排除的怀疑（写在这里以免执行者走错方向）：**尾部 VACUUM 不是瓶颈**。实测（scratch 库）原地 `VACUUM` 在 WAL 模式下的 WAL 不增长；源码路径是 `sqlite3RunVacuum` → `sqlite3BtreeCopyFile`（把 ATTACH 的临时库整页拷回主库文件，不经过 WAL）。且当时进程的 `-wal` 在涨、主库文件 mtime 停在 13:27、另一个连接读到 `PRAGMA user_version` 仍是 10 —— 事务根本没提交，说明卡在迁移语句本身。**本计划不动 VACUUM。**

本计划落地后：主清理一趟单调前进（两次表扫描量级），工具输出阶段每条语句都走索引（`SEARCH ... USING INDEX`），并且每一步都有进度日志。同一库的迁移从「数小时 / 跑不完」降到分钟级。

## Current state

### 主代理清理：平方级批处理（`pkg/state/schema_migrations.go:709-736`，全文）

```go
// deleteMainAgentStreamDeltasV11 removes the main agent's own stream deltas —
// the text fragments that carry no agent_id. A surface reloads the main agent
// from the transcript, so nothing reads them. The deletes run in bounded
// batches by sequence so a multi-million-row purge never grows one statement
// past what a single transaction can hold.
func deleteMainAgentStreamDeltasV11(ctx context.Context, conn *sql.Conn) (int64, error) {
	inList := sqlInListV11(streamDeltaTextTypesV11)
	var total int64
	for {
		res, err := conn.ExecContext(ctx, `
DELETE FROM fb_session_events WHERE sequence IN (
  SELECT sequence FROM fb_session_events
  WHERE event_type IN (`+inList+`)
    AND COALESCE(json_extract(payload_json, '$.agent_id'), '') = ''
  ORDER BY sequence LIMIT 50000)`)
		if err != nil {
			return total, fmt.Errorf("delete the main agent's stream deltas: %w", err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return total, err
		}
		total += n
		if n == 0 {
			return total, nil
		}
	}
}
```

### 工具输出清理：预扫 + 每 step 两条全表扫描（`pkg/state/schema_migrations.go:996-1064`，节选）

```go
func pruneToolOutputDeltasV11(ctx context.Context, conn *sql.Conn) (toolOutputPruneV11, error) {
	var out toolOutputPruneV11
	rows, err := conn.QueryContext(ctx, `
SELECT COALESCE(run_id, ''), COALESCE(json_extract(payload_json, '$.step_id'), ''), COUNT(*)
FROM fb_session_events
WHERE event_type = ?
GROUP BY COALESCE(run_id, ''), COALESCE(json_extract(payload_json, '$.step_id'), '')`, streamDeltaToolOutputV11)
	...
	for _, step := range steps {
		var completed int
		if err := conn.QueryRowContext(ctx, `
SELECT COUNT(*) FROM fb_session_events
WHERE COALESCE(run_id, '') = ? AND event_type = 'tool_call_completed'
  AND COALESCE(json_extract(payload_json, '$.step_id'), '') = ?`, step.runID, step.stepID).Scan(&completed); err != nil {
			return out, err
		}
		if completed > 0 {
			res, err := conn.ExecContext(ctx, `
DELETE FROM fb_session_events
WHERE COALESCE(run_id, '') = ? AND event_type = ?
  AND COALESCE(json_extract(payload_json, '$.step_id'), '') = ?`, step.runID, streamDeltaToolOutputV11, step.stepID)
			...
			out.deleted += n
			continue
		}
		merged, err := mergeStepToolOutputV11(ctx, conn, step.runID, step.stepID)
		...
	}
```

合并路径同样按 step 全表扫描（`pkg/state/schema_migrations.go:1066-1113`）：

```go
func mergeStepToolOutputV11(ctx context.Context, conn *sql.Conn, runID, stepID string) (int64, error) {
	rows, err := conn.QueryContext(ctx, `
SELECT sequence, COALESCE(json_extract(payload_json, '$.text'), '')
FROM fb_session_events
WHERE COALESCE(run_id, '') = ? AND event_type = ?
  AND COALESCE(json_extract(payload_json, '$.step_id'), '') = ?
ORDER BY sequence`, runID, streamDeltaToolOutputV11, stepID)
```

`mergeStepToolOutputV11` 只被 `pruneToolOutputDeltasV11` 调用（`grep -rn mergeStepToolOutputV11 --include='*.go' .` 仅定义 + 这一处调用）。

### 表上的索引（`pkg/state/schema.sql:275-287`）

```sql
CREATE TABLE fb_session_events (
  sequence INTEGER PRIMARY KEY AUTOINCREMENT,
  session_id TEXT NOT NULL REFERENCES fb_sessions(id) ON DELETE CASCADE,
  run_id TEXT REFERENCES fb_runs(id) ON DELETE CASCADE,
  event_id TEXT NOT NULL,
  event_type TEXT NOT NULL,
  payload_json TEXT NOT NULL DEFAULT '{}',
  occurred_at_ms INTEGER NOT NULL,
  UNIQUE (session_id, event_id)
) STRICT;
CREATE INDEX idx_fb_session_events_session ON fb_session_events(session_id);
CREATE INDEX idx_fb_session_events_session_type ON fb_session_events(session_id, event_type);
CREATE INDEX idx_fb_session_events_run_type ON fb_session_events(run_id, event_type) WHERE run_id IS NOT NULL;
```

**没有任何索引以 `event_type` 打头**，且 `payload_json` 里的 `agent_id` / `step_id` 没有表达式索引——这就是上面三条 `SCAN` 的成因。

### 已验证的修复方向（本计划采用的形状，均已在本仓库 schema 上跑过 `EXPLAIN QUERY PLAN`）

```
-- 主清理批边界（新增的游标语句）
SEARCH fb_session_events USING INTEGER PRIMARY KEY (rowid>? AND rowid<?)
-- 建了两个局部表达式索引之后：
--   chunks 读取
SEARCH fb_session_events USING INDEX v11_tool_chunk_step (<expr>=?)
--   completed 计数
SEARCH fb_session_events USING INDEX v11_tool_done_step (<expr>=?)
--   chunks 删除
SEARCH fb_session_events USING INDEX v11_tool_chunk_step (<expr>=?)
--   预扫 GROUP BY（仍是 index scan，但只读 25k 条索引项）
SCAN fb_session_events USING INDEX v11_tool_chunk_step
```

关键约束（踩过的坑，照做即可）：**局部索引的 WHERE 子句只有被查询的 WHERE「蕴含」时才会被使用，而绑定参数 `event_type = ?` 不算蕴含**。所以这些语句里的 `event_type` 必须是字面量（与 `sqlInListV11` 内联常量的既有做法一致），`step_id` / `run_id` 的值仍可绑定 `?`。

### 既有测试与夹具（`pkg/state/schema_migrations_test.go`）

- `streamDeltaFixtureV11(t, userVersion)`（`:2116-2207`）：从 `schemaSQL` 建库再插数据，覆盖「主代理自有增量 / 被 transcript 覆盖的执行 / worker 会话已消失 / 无 spawn / 工具输出有与没有 completed」五种形状；批量行数 `streamDeltaBulkV11 = 400`（`:2211`）。
- `TestMigrateAwayStoredStreamDeltas`（`:2233`）：端到端断言清理后的行、文本、计数、`freelist_count == 0` 与备份存在。
- 测试注入变量的既有先例：`var migrationFreeSpace = diskFreeSpace`（`:1451`），测试里直接替换并 `defer` 还原。本计划的 `mainDeltaBatchV11` 照此办理。
- 这是纯 SQL 文本拼装 + `?` 绑定的代码；`sqlInListV11` 的注释解释了为什么常量内联、值走绑定。

### 仓库约定

- 注释写完整英文句子、解释「为什么」；迁移进度日志用 `slog.Info`，字段名小写下划线（既有：`"main_agent_rows_deleted"`、`"freed_pages"`）。
- 迁移代码不许在语义上「顺手优化」：本计划之外的 SQL 一行都不要动。
- 测试用标准 `testing` + `t.Fatalf`；错误信息用 `fatalf`/`t.Fatalf`，与文件现有风格一致。

## Commands you will need

| Purpose | Command | Expected on success |
|---|---|---|
| 编译 | `CGO_ENABLED=1 go build ./...` | exit 0 |
| 本包测试（基线） | `CGO_ENABLED=1 go test -tags fts5 -count=1 ./pkg/state` | `ok  github.com/forebrain-harness/forebrain-harness/pkg/state` （基线约 21s；链接期 libtokenizers 的 macOS 版本 warning 是既有噪音） |
| v11 端到端 | `CGO_ENABLED=1 go test -tags fts5 -count=1 ./pkg/state -run 'TestMigrateAwayStoredStreamDeltas\|TestV11' -v` | 全 PASS |
| 计划断言 | `CGO_ENABLED=1 go test -tags fts5 -count=1 ./pkg/state -run TestV11ToolOutputStatementsSeek -v` | PASS |
| 方言对照（可选） | `cd /tmp && rm -f fx.db && sqlite3 fx.db < <repo>/pkg/state/schema.sql` 后用 `sqlite3 fx.db "EXPLAIN QUERY PLAN ..."` | 与 "Current state" 里的计划一致 |
| vet / gofmt | `CGO_ENABLED=1 go vet -tags fts5 ./pkg/state/...`、`gofmt -l pkg/state` | 无输出 |

## Scope

**In scope**（只改这三个文件）：
- `pkg/state/schema_migrations.go`
- `pkg/state/schema_migrations_test.go`
- `plans/README.md`（只改 048 的状态行）

**Out of scope**（看着相关，一律不许动）：
- `pkg/state/schema.sql` 与 `stateSchemaVersion`：本期不新增版本号，也不动既有索引。
- 尾部 `VACUUM` 与备份逻辑：`plans/049`、`plans/050` 负责；本计划一行不碰。
- Subagent 阶段（`pruneSubagentStreamDeltasV11` / `selectSubagentDeltaRunsV11` / `mergeRunStreamDeltasV11` / `workerTranscriptCoversV11`）：实测 `selectSubagentDeltaRunsV11` 的计划是 `SEARCH ... idx_fb_session_events_run_type (run_id>?)`（按 group 逐次 seek，本库 289 个执行体），逐 run 查询也都有索引，**不改**。只加进度日志。
- 任何「顺便重写 `mergeStepToolOutputV11` / 改成按 run 读取」的重构：本计划保留现有语句形状，只让它们走索引。

## Git workflow

- **不建分支、不 commit、不 push**：改动留在工作区（仓库现行做法：004–047 全部留在工作区由 owner 审阅）。
- 若将来提交，建议 message：`perf(state): make the v10→v11 stream-delta purge linear and index-driven`。

## Steps

### Step 1: 基线与漂移检查

```bash
git diff --stat a8140fc..HEAD -- pkg/state/schema_migrations.go pkg/state/schema_migrations_test.go
CGO_ENABLED=1 go test -tags fts5 -count=1 ./pkg/state
grep -n "func deleteMainAgentStreamDeltasV11" pkg/state/schema_migrations.go          # 1 行
grep -n "func mergeStepToolOutputV11" pkg/state/schema_migrations.go                   # 1 行
grep -rn "mergeStepToolOutputV11" --include='*.go' pkg cmd | wc -l                     # 2（定义 + 1 调用）
```

**Verify**: `git diff --stat` 对本计划 in-scope 的两个 `.go` 文件无输出；测试 `ok`。

### Step 2: 主代理清理改成单调游标

在 `pkg/state/schema_migrations.go` 里，用下面三个符号替换 `deleteMainAgentStreamDeltasV11`（`:709-736`）。**导入块需要加 `math`**（当前没有）。

```go
// mainDeltaBatchV11 is how many of the main agent's stream deltas one delete
// statement removes. It is a variable so a test can drive the purge across
// several batches on a small fixture.
var mainDeltaBatchV11 = 50000

// mainDeltaFilterV11 names the rows the main-agent purge removes: the stored
// text deltas that carry no agent_id. No index leads with event_type and the
// filter reads payload_json, so only a scan of fb_session_events can answer
// it — which is why the purge walks the primary key forward once instead of
// re-scanning the table from the first row per batch.
func mainDeltaFilterV11() string {
	return `event_type IN (` + sqlInListV11(streamDeltaTextTypesV11) + `)
    AND COALESCE(json_extract(payload_json, '$.agent_id'), '') = ''`
}

// deleteMainAgentStreamDeltasV11 removes the main agent's own stream deltas —
// the text fragments that carry no agent_id. A surface reloads the main agent
// from the transcript, so nothing reads them. The deletes run in bounded
// batches by sequence so a multi-million-row purge never grows one statement
// past what a single transaction can hold, and the cursor only moves forward:
// restarting the scan per batch would make the purge quadratic in the number
// of deltas, which is exactly the tax this migration exists to remove.
func deleteMainAgentStreamDeltasV11(ctx context.Context, conn *sql.Conn) (int64, error) {
	filter := mainDeltaFilterV11()
	var total int64
	after := int64(math.MinInt64)
	for {
		last, err := mainDeltaBatchEndV11(ctx, conn, after)
		if err != nil {
			return total, err
		}
		if !last.Valid {
			return total, nil
		}
		res, err := conn.ExecContext(ctx, `
DELETE FROM fb_session_events
WHERE sequence > ? AND sequence <= ? AND `+filter, after, last.Int64)
		if err != nil {
			return total, fmt.Errorf("delete the main agent's stream deltas: %w", err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return total, err
		}
		total += n
		slog.Info("state v10→v11 removed the main agent's stream deltas", "rows_deleted", total)
		after = last.Int64
	}
}

// mainDeltaBatchEndV11 returns the sequence of the last row of the next batch
// of matching rows above after, or NULL when none is left. The subquery scans
// forward from after and stops at the batch boundary, so every row is visited
// by one batch-selection and at most one delete: the whole purge costs about
// two passes over the table instead of one pass per batch.
func mainDeltaBatchEndV11(ctx context.Context, conn *sql.Conn, after int64) (sql.NullInt64, error) {
	var last sql.NullInt64
	if err := conn.QueryRowContext(ctx, `
SELECT MAX(sequence) FROM (
  SELECT sequence FROM fb_session_events
  WHERE sequence > ? AND `+mainDeltaFilterV11()+`
  ORDER BY sequence LIMIT ?)`, after, mainDeltaBatchV11).Scan(&last); err != nil {
		return last, fmt.Errorf("find the next batch of the main agent's stream deltas: %w", err)
	}
	return last, nil
}
```

语义核对（执行者必须自己确认这四条，它们是「逐字不变」的证据）：
1. 删除的集合 = 同一 filter 的全部行，分批推进、每批 ≤ `mainDeltaBatchV11` 行；
2. 每批的 DELETE 一定删掉该批 SELECT 找到的每一行（同一 filter，且行都在 `(after, last]` 内）；
3. 当没有更多匹配行时 `last` 为 NULL → 返回 `total`（不再依赖「本批删了 0 行」）；
4. 计数 `total` 与旧实现相同（旧实现 `total += n` 到 `n == 0`；新实现累加到没有下一批）。

**Verify**:
```bash
grep -n "func mainDeltaBatchEndV11" pkg/state/schema_migrations.go        # 1 行
grep -n "var mainDeltaBatchV11 = 50000" pkg/state/schema_migrations.go    # 1 行
cd /tmp && sqlite3 fx.db "EXPLAIN QUERY PLAN DELETE FROM fb_session_events WHERE sequence > ? AND sequence <= ? AND event_type IN ('assistant_delta','reasoning_delta','reasoning_done') AND COALESCE(json_extract(payload_json,'\$.agent_id'),'') = '';"
```
→ 最后一条输出必须是 `SEARCH fb_session_events USING INTEGER PRIMARY KEY (rowid>? AND rowid<?)`（`fx.db` 用 Step 1 的 `sqlite3 fx.db < pkg/state/schema.sql` 建）。

### Step 3: 两条迁移局部索引 + 语句内联事件类型字面量

**3a.** 在 `streamDeltaToolOutputV11` 常量（`:521` 附近）之后新增：

```go
// v11ToolChunkIndexDDL and v11ToolDoneIndexDDL are the migration-local
// indexes that make the v11 tool-output purge seek instead of scan. Its
// statements filter on COALESCE(run_id, '') and on a step_id read out of
// payload_json, and no declared index answers either, so on a library with
// millions of events each statement was a full scan — once per tool step.
// The event type is part of the index's WHERE clause, which is why the
// statements below name the same literal: a bound parameter is invisible to
// the planner, and the partial index would not match. The pair is dropped
// when the step ends; nothing in the schema has to carry their write cost.
const v11ToolChunkIndexDDL = `CREATE INDEX IF NOT EXISTS v11_tool_chunk_step
ON fb_session_events(COALESCE(run_id, ''), json_extract(payload_json, '$.step_id'))
WHERE event_type = 'tool_output_delta'`

const v11ToolDoneIndexDDL = `CREATE INDEX IF NOT EXISTS v11_tool_done_step
ON fb_session_events(COALESCE(run_id, ''), json_extract(payload_json, '$.step_id'))
WHERE event_type = 'tool_call_completed'`
```

**3b.** 在同一区域新增三条语句常量，并把 `pruneToolOutputDeltasV11` / `mergeStepToolOutputV11` 里的对应内联 SQL 换成它们（只换文本来源，绑定参数不变）：

```go
// The three statements the tool-output purge runs per step. They live here as
// constants so the plan test can EXPLAIN the very statements the migration
// runs; a copy would drift and stop pinning anything.
const v11ToolCompletedSQL = `SELECT COUNT(*) FROM fb_session_events
WHERE COALESCE(run_id, '') = ? AND event_type = 'tool_call_completed'
  AND COALESCE(json_extract(payload_json, '$.step_id'), '') = ?`

const v11ToolChunkDeleteSQL = `DELETE FROM fb_session_events
WHERE COALESCE(run_id, '') = ? AND event_type = 'tool_output_delta'
  AND COALESCE(json_extract(payload_json, '$.step_id'), '') = ?`

const v11ToolChunksSQL = `SELECT sequence, COALESCE(json_extract(payload_json, '$.text'), '')
FROM fb_session_events
WHERE COALESCE(run_id, '') = ? AND event_type = 'tool_output_delta'
  AND COALESCE(json_extract(payload_json, '$.step_id'), '') = ?
ORDER BY sequence`
```

**3c.** `pruneToolOutputDeltasV11` 开头建索引、结尾删索引（每次都 `IF NOT EXISTS` / `IF EXISTS`，重复执行安全；删索引用普通错误返回，**不要 defer 吞错**——吞掉会让一个 schema.sql 未声明的索引留在库里，而整个迁移本就在一个事务里，失败即回滚）：

```go
func pruneToolOutputDeltasV11(ctx context.Context, conn *sql.Conn) (toolOutputPruneV11, error) {
	var out toolOutputPruneV11
	for _, ddl := range []string{v11ToolChunkIndexDDL, v11ToolDoneIndexDDL} {
		if _, err := conn.ExecContext(ctx, ddl); err != nil {
			return out, fmt.Errorf("create the v11 tool-output index: %w", err)
		}
	}
	...
	// 处理循环结束后：
	for _, name := range []string{"v11_tool_chunk_step", "v11_tool_done_step"} {
		if _, err := conn.ExecContext(ctx, `DROP INDEX IF EXISTS `+name); err != nil {
			return out, fmt.Errorf("drop the v11 tool-output index %s: %w", name, err)
		}
	}
	return out, nil
}
```

**3d.** 预扫的分组语句里把 `WHERE event_type = ?` 改成 `WHERE event_type = 'tool_output_delta'`（参数列表去掉 `streamDeltaToolOutputV11`）。同一条规则：参数不算蕴含，局部索引不会被选中。

**3e.** `pruneToolOutputDeltasV11` 的两条语句与 `mergeStepToolOutputV11` 的读取语句换成 3b 的常量（绑定参数维持 `step.runID, step.stepID` 的顺序）。

**Verify**:
```bash
grep -c "v11_tool_chunk_step\|v11_tool_done_step" pkg/state/schema_migrations.go   # ≥ 6（两条 DDL + 两个 DROP 名 + 常量注释）
grep -n "WHERE event_type = ?" pkg/state/schema_migrations.go | wc -l             # 工具输出相关的应为 0
cd /tmp && sqlite3 fx2.db "EXPLAIN QUERY PLAN SELECT COUNT(*) FROM fb_session_events WHERE COALESCE(run_id, '') = ? AND event_type = 'tool_call_completed' AND COALESCE(json_extract(payload_json,'\$.step_id'),'') = ?;"
```
→ `SEARCH fb_session_events USING INDEX v11_tool_done_step (<expr>=?)`（`fx2.db` 为 Step 1 的 schema 库 + 用 3a 的两条 DDL 建过索引）。

### Step 4: 三个子步骤的进度日志

风格照 `migrateStateV10ToV11` 现有的汇总行（`:541-550`），字段名小写下划线：

- `migrateStateV10ToV11` 开头一行：`slog.Info("state v10→v11 removing the stored stream deltas")`——这是「一小时零输出」的直接修复。
- 主清理：Step 2 已给（每批一行 `rows_deleted`）。
- `pruneSubagentStreamDeltasV11`：`for i, run := range runs` 里每 50 个 run 打一行 `slog.Info("state v10→v11 pruned execution stream deltas", "executions", i+1, "deleted", out.deleted, "merged_executions", out.mergedExecutions, "without_spawn", out.withoutSpawn)`。
- `pruneToolOutputDeltasV11`：`for i, step := range steps` 里每 200 个 step 打一行 `slog.Info("state v10→v11 pruned tool-output deltas", "steps", i+1, "deleted", out.deleted, "merged_steps", out.mergedSteps)`。

**Verify**: `CGO_ENABLED=1 go test -tags fts5 -count=1 ./pkg/state -run TestMigrateAwayStoredStreamDeltas -v 2>&1 | grep -c "state v10→v11"` → ≥ 3（起始行 + 各阶段至少各一行的汇总仍在；计数因夹具规模而异，只要 ≥ 3 即可）。

### Step 5: 测试

**5a.** 多批次端到端：在 `TestMigrateAwayStoredStreamDeltas` 的第一个子测试里，把批大小降下来（照 `migrationFreeSpace` 的注入惯例）：

```go
		oldBatch := mainDeltaBatchV11
		mainDeltaBatchV11 = 7
		defer func() { mainDeltaBatchV11 = oldBatch }()
```
放在 `streamDeltaFixtureV11` 之后、`Open` 之前。现有断言不改（`before != 29+streamDeltaBulkV11` 等），它们现在同时证明了「跨多个批次」的删除结果不变。

**5b.** 游标单元测试（同一文件末尾新增），直接钉住单调性与批次划分：

```go
// The main-agent purge must not re-scan what it has already settled: the batch
// cursor walks the primary key forward, so every matching row is named by
// exactly one batch end and the ends strictly increase. Restarting the scan
// per batch is what made a real library's migration run for hours.
func TestMainDeltaBatchCursorV11(t *testing.T) {
	...（用 streamDeltaFixtureV11(t, 10) 建库，直接 sql.Open(sqliteDriverName(), path) 打开，
	     把 mainDeltaBatchV11 设为 3，反复调用 mainDeltaBatchEndV11 收集批边界：
	     - 边界严格递增；
	     - 每一批覆盖的匹配行数与预期一致（用 mainDeltaFilterV11 计数）；
	     - 结束时 last 无效（NULL）；
	     - 把所有边界对应的区间行数相加 == 库里匹配行总数（每行恰好属于一批）。）
}
```

**5c.** 计划断言（同一文件末尾新增）：建一个 `schemaSQL` 库 + 用 `v11ToolChunkIndexDDL` / `v11ToolDoneIndexDDL` 建索引，然后对三条语句常量与实际使用的预扫语句跑 `EXPLAIN QUERY PLAN`，逐条断言：

```go
// Small fixtures cannot show a scan that costs seconds on a real library, so
// this pins the access path instead of the wall clock: each statement the v11
// tool-output purge runs must be answered by one of its two migration-local
// indexes.
func TestV11ToolOutputStatementsSeek(t *testing.T) {
	// 对每条语句（v11ToolCompletedSQL / v11ToolChunkDeleteSQL / v11ToolChunksSQL / 预扫 GROUP BY）：
	//   rows, err := db.Query("EXPLAIN QUERY PLAN "+stmt, args...)   // args 用任意值，如 "r", "s"
	//   汇总 detail 文本；
	//   断言 strings.Contains(plan, "v11_tool_done_step") 或 "v11_tool_chunk_step"；
	//   对三条 SEARCH 语句额外断言 !strings.Contains(plan, "SCAN fb_session_events")；
	//   对预扫语句允许 "SCAN ... USING INDEX v11_tool_chunk_step"（它只读索引项），
	//   但必须包含 "USING INDEX v11_tool_chunk_step"。
	// 另加一条护栏：同一批语句在不建索引的库上会退化成 SCAN —— 断言计划里不含 v11_tool_* 时确实出现
	// "SCAN fb_session_events"，这样「索引是修复的一半」这件事本身也被钉住。
}
```

**5d.** 夹具补 `run_id IS NULL` 形状：`fb_session_events.run_id` 可为 NULL，而 `COALESCE(run_id,'')=''` 这一族语句正要把 NULL 组也处理掉（表达式索引的键同样是 COALESCE，所以 NULL 组天然落在索引里）。在 `streamDeltaFixtureV11` 的 `rows` 里追加一组 `run_id` 为 NULL 的工具输出（一条有 completed 的 step、一条没有），并把该 fixture 的固定计数同步更新：`before` 由 `29+streamDeltaBulkV11` 调整为实际值、迁移后的 `countStreamDeltas(...) != 7` 同样按新增行数修正（新增：有 completed 的 3 块 + 无 completed 的 2~3 块；具体数字由执行者按插入行数算清后写死，并在注释里写明「主代理 NULL run 组：删除 / 合并」）。若新增行让 `TestMigrateAwayStoredStreamDeltas` 的其它断言（如 `freelist_count`）成立性变化，按实际调整并在此报告。

**Verify**: `CGO_ENABLED=1 go test -tags fts5 -count=1 ./pkg/state -run 'TestMigrateAwayStoredStreamDeltas|TestMainDeltaBatchCursorV11|TestV11ToolOutputStatementsSeek' -v` → 全 PASS；且 `git stash` 掉源码改动后 `TestV11ToolOutputStatementsSeek` 应当 FAIL（证明它不是空断言，报告里附这条对照）。

### Step 6: 规模证据（不进测试套件）

在 `/tmp` 写一个一次性程序（不提交），用与 `streamDeltaFixtureV11` 相同的手法造一个 **v10** 大夹具：约 20 万条主代理增量 + 2 万条工具输出块（分布在若干千个 step）+ 若干非增量行，`user_version` 设 10，然后：

1. 在 **未修复** 的 HEAD 上跑 `state.Open`，用 `time` 记墙钟（预期：分钟级且随规模平方增长；若在合理时间内跑不完，记下它在多长时间后被杀掉，不要等它跑完）；
2. 在修复后的工作区上重复，记墙钟（预期：秒级到分钟级，且线性）。

两条数字写进交付报告。**不要**把这个程序或它的夹具提交进仓库。

## Test plan

- 新增：`TestMainDeltaBatchCursorV11`（批游标单调、覆盖恰好一次）、`TestV11ToolOutputStatementsSeek`（四条语句的计划断言 + 索引缺失时的退化护栏）。
- 修改：`TestMigrateAwayStoredStreamDeltas`（批大小降到 7 跑多批次；夹具增补 `run_id IS NULL` 的工具输出形状并同步固定计数）。**语义断言逐字保留**。
- 结构范本：`streamDeltaFixtureV11`（`:2116`）与 `TestMigrateAwayStoredStreamDeltas`（`:2233`）。
- 验证：`CGO_ENABLED=1 go test -tags fts5 -count=1 ./pkg/state` → `ok`；随后 `CGO_ENABLED=1 go test -tags fts5 -count=1 ./pkg/... ./cmd/...` 不得有新增失败（`pkg/mcp` 的 `TestStartStdioConnectsRealMCPServerProcess` 是既有失败，与本计划无关）。

## Done criteria

ALL must hold:

- [ ] `CGO_ENABLED=1 go test -tags fts5 -count=1 ./pkg/state` → `ok`
- [ ] `grep -c "func mainDeltaBatchEndV11" pkg/state/schema_migrations.go` → `1`
- [ ] `grep -n "ORDER BY sequence LIMIT 50000" pkg/state/schema_migrations.go` → 无输出（平方级批处理已消失）
- [ ] `grep -c "v11_tool_chunk_step" pkg/state/schema_migrations.go` → `≥ 3`；`grep -c "v11_tool_done_step"` → `≥ 3`
- [ ] `grep -n "WHERE event_type = ?" pkg/state/schema_migrations.go` → 无输出
- [ ] `EXPLAIN QUERY PLAN` 四条语句（Step 3/5c 的命令）全部出现 `USING INDEX v11_tool_*`，三条 SEARCH 语句无 `SCAN fb_session_events`
- [ ] `CGO_ENABLED=1 go vet -tags fts5 ./pkg/state/...` 与 `gofmt -l pkg/state` 无输出
- [ ] `git status --short` 只多出 `pkg/state/schema_migrations.go`、`pkg/state/schema_migrations_test.go`、`plans/README.md`
- [ ] 交付报告含：Step 6 的修复前后墙钟对照、`TestV11ToolOutputStatementsSeek` 的 stash 对照输出、以及「清理集合与计数未变」的核对说明
- [ ] `plans/README.md` 中 048 的状态行更新

## STOP conditions

Stop and report back (do not improvise) if:

- Drift check 发现 in-scope 文件在 a8140fc 之后被改过，且 "Current state" 摘录与实码对不上。
- 建立局部表达式索引失败（例如 `json_extract` 在该 SQLite 构建下不被视为可索引的确定性函数），或 `EXPLAIN` 显示语句 **仍然**是 `SCAN fb_session_events`（不带 `USING INDEX`）——这说明修复方向不成立，不要退而求其次去改语义。
- 现有测试 `TestMigrateAwayStoredStreamDeltas` 除「固定计数随夹具更新」之外的任何断言需要改动才能通过——那说明清理语义被改变了，停下报告。
- 任何步骤的验证命令连续两次失败且原因不明。
- 需要触碰 Out of scope 的文件（尤其 `schema.sql` / 版本号 / VACUUM / 备份逻辑）才能让测试变绿。
- Step 6 的大夹具在**修复后**仍然跑不出线性结果（预期与实际差一个数量级以上）——把两次的 wc/时间数据贴出来报告。

## Maintenance notes

- **不变式**：v11 的清理语句与它的访问路径是一对——`event_type` 在这些语句里必须保持字面量，否则两个局部索引静默失效，`TestV11ToolOutputStatementsSeek` 会失败（这是刻意的）。
- **将来若要再动这块**：`mainDeltaBatchV11` 是测试注入点；批边界由 `mainDeltaBatchEndV11` 单独负责，别把「找批」和「删批」合并回一条语句（那会把游标退化成重新扫描）。
- **审阅重点**：索引的建立/删除是否都在 `pruneToolOutputDeltasV11` 内（不得泄漏到 schema.sql）；`DROP INDEX` 的错误是否向上返回；`total` / `out.deleted` / `out.mergedRows` / `out.withoutStepID` 的含义与旧实现逐字一致。
- **明确不做**：尾部 VACUUM 与备份策略见 049/050；VACUUM INTO 替换原地 VACUUM 的提议已评估并拒绝（原因见 `plans/README.md`「已考虑并拒绝的发现」）。
