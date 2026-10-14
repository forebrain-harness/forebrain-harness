# Plan 052: v10→v11 迁移每个阶段都留下进度日志——「静默 90 分钟」不再发生

> **REJECTED（owner 2026-10-14 裁决，不要执行）**：不做。理由：本计划的价值只落在「下一个大库的首次 v10→v11 升级」上——那是一次 `main_agent_rows_deleted=0`、subagent 阶段耗时 93 分钟的迁移，而它已经在唯一已知的受影响库上跑完了。方案原样保留在本文件（将来真有第二个大库要升 v11 时可直接复活），当前不投入。

> **Executor instructions**: Follow this plan step by step. Run every
> verification command and confirm the expected result before moving to the
> next step. If anything in the "STOP conditions" section occurs, stop and
> report — do not improvise. When done, update the status row for this plan
> in `plans/README.md`.
>
> 本计划**只加日志**：不重写任何清理语句、不动 VACUUM / 备份 / 索引 / 版本号。清理集合与计数必须逐字不变。
>
> **Drift check (run first)**: `git diff --stat a8140fc..HEAD -- pkg/state/schema_migrations.go pkg/state/schema_migrations_test.go`
> 有改动就先把 "Current state" 的摘录与实码对照；对不上按 STOP 处理。

## Status

- **Priority**: P3（只对「下一个大库」有价值；owner 自己的库已迁移完成）
- **Effort**: S（约 20 行 + 一个测试）
- **Risk**: LOW（纯日志；唯一的行为改动是循环变量加下标）
- **Depends on**: none
- **Category**: dx / perf
- **Planned at**: commit `a8140fc`, 2026-10-14

## Why this matters

owner 的库实测：13:31:43 打出「backed up the state database before migrating」，直到 **15:05:15** 才打出 v11 的汇总行——**93 分 32 秒，全程零输出**。这份静默直接引发了本次调查（进程被判断为「卡死」，还提了 VACUUM INTO 的假设）。而迁移其实一直在正常干活：

```
state v10→v11 removed the stored stream deltas main_agent_rows_deleted=0
  subagent_rows_deleted=1025186 subagent_executions_merged=233
  subagent_rows_merged_away=6493409 subagent_executions_without_spawn=0
  tool_output_rows_deleted=25155 tool_steps_merged=7 tool_output_rows_merged_away=5
```

唯一的问题就是这行汇总**只在全部阶段结束后**才打印（`pkg/state/schema_migrations.go:541`）。任何用户升到 v11 的库（只要用过 subagent）都会经历同样时长的静默。加四条日志（一条起始 + 每阶段一条节奏行）就把「是不是挂了」变成「第几阶段、做了多少」。

本计划是 048 被驳回后唯一保留的部分（048 的另外三项——主清理游标重写、两条局部表达式索引——已因实测数据证伪其收益而被撤回，见 `plans/README.md`）。

## Current state

### 三个阶段都是静默循环

`migrateStateV10ToV11`（`:528-552`）只在最后打印汇总：

```go
func migrateStateV10ToV11(ctx context.Context, conn *sql.Conn) error {
	mainDeleted, err := deleteMainAgentStreamDeltasV11(ctx, conn)
	...
	subagent, err := pruneSubagentStreamDeltasV11(ctx, conn)
	...
	tool, err := pruneToolOutputDeltasV11(ctx, conn)
	...
	slog.Info("state v10→v11 removed the stored stream deltas",
		"main_agent_rows_deleted", mainDeleted, ...)
	return nil
}
```

主清理的批循环（`:714-736`）没有日志：

```go
	var total int64
	for {
		res, err := conn.ExecContext(ctx, `DELETE FROM fb_session_events WHERE sequence IN (...)`)
		...
		total += n
		if n == 0 {
			return total, nil
		}
	}
```

subagent 阶段按执行体循环（`:759-799`，节选）——**实测这里消耗了 93 分钟里的绝大部分**（233 个执行体、6,493,409 行被合并掉、1,025,186 行被删）：

```go
	runs, err := selectSubagentDeltaRunsV11(ctx, conn)
	...
	for _, run := range runs {
		worker, found, err := subagentWorkerSessionV11(ctx, conn, run)
		...
	}
	return out, nil
```

工具输出阶段按 step 循环（`:1033-1062`，节选）：

```go
	for _, step := range steps {
		var completed int
		if err := conn.QueryRowContext(ctx, `SELECT COUNT(*) ...`).Scan(&completed); err != nil {
		...
	}
	return out, nil
```

### 既有日志与测试风格

- 汇总行（`:541`）用 `slog.Info`，字段名小写下划线，消息前缀固定为 `state v10→v11 ...`。
- 测试捕获日志的既有先例（照它抄）：`pkg/gateway/http_server_test.go:184-185`

```go
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })
```

## Commands you will need

| Purpose | Command | Expected on success |
|---|---|---|
| 编译 | `CGO_ENABLED=1 go build ./...` | exit 0 |
| 本包测试 | `CGO_ENABLED=1 go test -tags fts5 -count=1 ./pkg/state` | `ok` |
| 新测试 | `CGO_ENABLED=1 go test -tags fts5 -count=1 ./pkg/state -run TestV11MigrationReportsItsProgress -v` | PASS |
| vet / gofmt | `CGO_ENABLED=1 go vet -tags fts5 ./pkg/state/...`、`gofmt -l pkg/state` | 无输出 |

（所有命令在工作区根执行；若在隔离 worktree 中执行，先 `cd` 进去并带 `FOREBRAIN_HOME=$(mktemp -d)`。）

## Scope

**In scope**：
- `pkg/state/schema_migrations.go`
- `pkg/state/schema_migrations_test.go`
- `plans/README.md`（只改 052 的状态行）

**Out of scope**（一律不许动）：
- 任何清理语句（DELETE/SELECT 的文本与绑定参数）、`mainDeltaBatchV11`、索引、`schema.sql`、版本号。
- `:541` 的汇总行（保留原样，字段名一个都不改）。
- VACUUM / 备份 / 空间预检（049/050 已驳回）。
- 逐行日志（会把控制台刷爆）：节奏必须按「批」计数。

## Steps

### Step 1: 基线与漂移检查

```bash
git diff --stat a8140fc..HEAD -- pkg/state/schema_migrations.go pkg/state/schema_migrations_test.go
CGO_ENABLED=1 go test -tags fts5 -count=1 ./pkg/state
grep -c "slog.Info(\"state v10→v11" pkg/state/schema_migrations.go     # 1（只有汇总行）
```

**Verify**: 无 drift；测试 `ok`；grep 为 1。

### Step 2: 四条日志

**2a. 起始行**（`migrateStateV10ToV11` 开头，三个调用之前）：

```go
	// An hour-long migration with no output reads as a hang — this line and
	// the per-phase lines below are what turns "is it stuck?" into "which
	// phase, how far".
	slog.Info("state v10→v11 removing the stored stream deltas")
```

**2b. 主清理每批一行**（`deleteMainAgentStreamDeltasV11` 的 `total += n` 之后）：

```go
		slog.Info("state v10→v11 removing the main agent's stream deltas", "rows_deleted", total)
```

**2c. subagent 阶段**（`pruneSubagentStreamDeltasV11`：把 `for _, run := range runs` 改成带下标的循环，并在循环体开头加节奏行）：

```go
	for i, run := range runs {
		// Every 25 executions, and on the first one: a library with a heavy
		// subagent history spends most of this migration in this loop.
		if i == 0 || (i+1)%25 == 0 {
			slog.Info("state v10→v11 pruning execution stream deltas",
				"executions_done", i+1, "executions_total", len(runs),
				"deleted", out.deleted, "merged_executions", out.mergedExecutions,
				"rows_merged_away", out.mergedRows)
		}
		...
	}
```

**2d. 工具输出阶段**（`pruneToolOutputDeltasV11`：同样加下标与节奏行）：

```go
	for i, step := range steps {
		if i == 0 || (i+1)%200 == 0 {
			slog.Info("state v10→v11 pruning tool-output deltas",
				"steps_done", i+1, "steps_total", len(steps),
				"deleted", out.deleted, "merged_steps", out.mergedSteps)
		}
		...
	}
```

**Verify**:
```bash
grep -c "slog.Info(\"state v10→v11" pkg/state/schema_migrations.go   # 5（起始 + 主批 + 两个节奏 + 原有汇总）
CGO_ENABLED=1 go test -tags fts5 -count=1 ./pkg/state
```
→ 测试仍 `ok`（除日志外没有任何行为变化）。

### Step 3: 测试

在 `schema_migrations_test.go` 末尾新增（捕获手法照 `pkg/gateway/http_server_test.go:184-185`）：

```go
// The v11 migration took 93 minutes on a real library and said nothing while
// it ran; the phase summary only arrives when the work is already done. Every
// phase must report at least once, on the fixture that exercises all of them.
func TestV11MigrationReportsItsProgress(t *testing.T) {
	ctx := context.Background()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })

	path := streamDeltaFixtureV11(t, 10)
	migrated, err := Open(ctx, path, nil)
	if err != nil {
		t.Fatalf("open the v10 fixture: %v", err)
	}
	defer migrated.Close()

	logs := buf.String()
	for _, want := range []string{
		"state v10→v11 removing the stored stream deltas",
		"state v10→v11 removing the main agent's stream deltas",
		"state v10→v11 pruning execution stream deltas",
		"state v10→v11 pruning tool-output deltas",
	} {
		if !strings.Contains(logs, want) {
			t.Fatalf("the migration log is missing %q; captured:\n%s", want, logs)
		}
	}
	if !strings.Contains(logs, "state v10→v11 removed the stored stream deltas") {
		t.Fatalf("the phase summary is missing; captured:\n%s", logs)
	}
}
```

（`bytes` / `log/slog` 若不在测试文件的导入块里就补上；`strings`、`context` 已有。）

**Verify**: `CGO_ENABLED=1 go test -tags fts5 -count=1 ./pkg/state -run TestV11MigrationReportsItsProgress -v` → PASS；随后 `CGO_ENABLED=1 go test -tags fts5 -count=1 ./pkg/state` → `ok`。

## Test plan

- 新增：`TestV11MigrationReportsItsProgress`（四条阶段日志 + 汇总行都出现）。
- 既有：`TestMigrateAwayStoredStreamDeltas` 等全部原样通过（本计划不改清理语义）。
- 验证：`CGO_ENABLED=1 go test -tags fts5 -count=1 ./pkg/state` → `ok`。

## Done criteria

ALL must hold:

- [ ] `CGO_ENABLED=1 go test -tags fts5 -count=1 ./pkg/state` → `ok`，且新测试 PASS
- [ ] `grep -c "slog.Info(\"state v10→v11" pkg/state/schema_migrations.go` → `5`
- [ ] `git diff --stat` 只列出 `pkg/state/schema_migrations.go`、`pkg/state/schema_migrations_test.go`（外加 `plans/README.md`）
- [ ] `git diff pkg/state/schema_migrations.go` 中没有任何 DELETE/SELECT 语句文本的变化（只有 slog 行与循环下标）
- [ ] `CGO_ENABLED=1 go vet -tags fts5 ./pkg/state/...` 与 `gofmt -l pkg/state` 无输出
- [ ] `plans/README.md` 中 052 的状态行更新

## STOP conditions

Stop and report back (do not improvise) if:

- Drift check 发现 in-scope 文件在 a8140fc 之后有改动且摘录对不上。
- 需要改动任何清理语句、索引、`schema.sql`、版本号才能加日志。
- 节奏行在既有夹具上一条都不出现（说明下标或循环结构判断错误）。
- 既有测试需要改断言才能通过。

## Maintenance notes

- **节奏常量**：25（执行体）/ 200（step）/ 每批（主清理）是「日志行数有界」的保证；加新阶段时照此挑一个数量级，不要改成逐行。
- **审阅重点**：四条消息文本互不为子串（否则 Step 3 的断言会互相满足）；`executions_total` / `steps_total` 用的是循环所在切片的长度；`:541` 的汇总行字段名未动。
- **明确不做**：048 的游标与索引改动已撤回（实测无收益，见 `plans/README.md`）；049/050 已驳回。
