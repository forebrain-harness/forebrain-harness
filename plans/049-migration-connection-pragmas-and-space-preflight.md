# Plan 049: 迁移连接带上页缓存与 WAL 限额，磁盘预检按「备份 + WAL」计算

> **REJECTED（2026-10-14 复评，不要执行）**：迁移自己跑完的实测数据显示本计划的三项都没有标的——(1) 93 分钟是行级删除与合并（≈1,340 行/秒），不是缺缓存；(2) `-wal` 在最后一个连接关闭时已由 SQLite 清理（复评时 `~/.forebrain/state/` 已无 WAL 文件），限额针对的是临时占用而非泄漏；(3) 现有 1.1× 预检在 16 GiB 可用空间下放行了整段迁移且未失败，提到 2.2× 反而会把「空间其实够」的用户挡在门外。理由与数据见 `plans/README.md` 顶部复评与「已考虑并拒绝的发现」。
>
> **Executor instructions**: Follow this plan step by step. Run every
> verification command and confirm the expected result before moving to the
> next step. If anything in the "STOP conditions" section occurs, stop and
> report — do not improvise. When done, update the status row for this plan
> in `plans/README.md`.
>
> 本计划只调「迁移这台机器自己的设置」：连接级 PRAGMA 与预检数字。**不动任何一条迁移语句、不动 VACUUM、不动备份文件本身**（后两者是 `plans/050`）。
>
> **Drift check (run first)**: `git diff --stat a8140fc..HEAD -- pkg/state/schema_migrations.go`
> 若该文件在 a8140fc 之后有改动（`plans/048` 会改它，属预期），先把 "Current state" 的摘录与实码逐段对照；**048 的改动不冲突**（它在 `deleteMainAgentStreamDeltasV11` / `pruneToolOutputDeltasV11` 区域，本计划在 `migrateStateSchema` / `backupBeforeStreamDeltaMigrationV11` 区域），对不上才按 STOP 处理。

## Status

- **Priority**: P2
- **Effort**: S（约 40 行含注释与一处测试调整）
- **Risk**: LOW（都是连接级设置，作用域限于迁移连接，结束即还原；预检数字只影响「早拒绝」的阈值）
- **Depends on**: none（与 `plans/048` 可并行；两处代码区域不重叠，但都在 `schema_migrations.go`，建议顺序执行以免合并噪音）
- **Category**: perf / migration
- **Planned at**: commit `a8140fc`, 2026-10-14

## Why this matters

实测的迁移现场（5.6 GB 库、7.69M 行事件表）：

1. **迁移连接用的是驱动的 2 MiB 默认页缓存。** DSN（`pkg/state/db.go:154`）设了 `_journal_mode=WAL`、`_synchronous=NORMAL`、`_busy_timeout`、`_foreign_keys=1`、`_txlock=immediate`，都没有 `_cache_size`；`migrateStateSchema` 只把 `busy_timeout` 提到 600000 并关掉 `foreign_keys`。驱动内置默认值经核对为 `SQLITE_DEFAULT_CACHE_SIZE -2000`（2 MiB，amalgamation `sqlite3-binding.c:14851-14852`），`temp_store` 也仍是文件（`SQLITE_TEMP_STORE=1`）。迁移要跑「整表扫 + 每行 `json_extract`」的循环、还要做大 GROUP BY 排序，2 MiB 缓存意味着反复把刚读过的页淘汰掉再读回来。
2. **迁移的 WAL 没有上限。** `journal_size_limit` 默认 -1（不限制），确认点后 WAL 文件**不会被截断**，停留在高水位。owner 机器上此刻的 `forebrain.state.sqlite-wal` 已达 3.1 GB 且在涨；v11 清理提交时这个文件还会更大，并且此后一直占着磁盘。
3. **预检低估了需要多少空间。** `backupBeforeStreamDeltaMigrationV11` 的预检是 `need = fileSize × 1.1`，注释说明它只覆盖 `VACUUM INTO` 那份副本。但整个迁移是**一个事务**：它把所有脏页写进 WAL（WAL 与库文件同盘），v11 清理会重写大部分页，所以真实需求是「副本 + 一份接近库大小的 WAL」。owner 机器 16 GiB 可用、库 5.6 GiB：按 2.2× 计算需要 12.3 GiB——能过，但按今天的 1.1× 只检查 6.2 GiB，剩下的空间债要在迁移中途还，还不上就是「跑了半小时然后回滚」。

落地后：迁移连接的缓存与 WAL 限额与它的工作量匹配，预检在**开跑前**就把「备份 + WAL」算进去，失败信息说清两个组成部分。

## Current state

### 迁移连接的 PRAGMA 区（`pkg/state/schema_migrations.go:1554-1580`，节选）

```go
	if _, err := conn.ExecContext(ctx, `PRAGMA busy_timeout = 600000`); err != nil {
		return err
	}
	// The pool hands this connection to ordinary callers next; they wait as
	// long as the DSN promises, not as long as a migration may.
	defer func() {
		if _, pragmaErr := conn.ExecContext(context.Background(), fmt.Sprintf(`PRAGMA busy_timeout = %d`, stateBusyTimeoutMs)); err == nil && pragmaErr != nil {
			err = pragmaErr
		}
	}()
	// The setting is a no-op inside a transaction, so it belongs here, and the
	// migration rewrites tables wholesale instead of editing them row by row.
	if _, err := conn.ExecContext(ctx, `PRAGMA foreign_keys = OFF`); err != nil {
		return err
	}
```

这就是本计划要照抄的「提升某设置 → defer 还原」模式。

### DSN 的既有设置（`pkg/state/db.go:154`）

```go
		RawQuery: fmt.Sprintf("mode=rwc&_busy_timeout=%d&_journal_mode=WAL&_synchronous=NORMAL&_foreign_keys=1&_txlock=immediate", stateBusyTimeoutMs),
```

驱动**不支持** `_journal_size_limit`（`grep -oE '"_?(cache_size|journal_size_limit|mmap_size|temp_store)"' .../go-sqlite3@v1.14.52/sqlite3.go` 只命中 `_cache_size`），所以 WAL 限额只能走连接级 PRAGMA——这也正好是本计划想要的范围。

### 预检与错误信息（`pkg/state/schema_migrations.go:1489-1502`）

```go
	info, err := os.Stat(dbPath)
	if err != nil {
		return fmt.Errorf("stat the state database for backup: %w", err)
	}
	need := int64(float64(info.Size()) * 1.1)
	stateDir := filepath.Dir(dbPath)
	free, err := migrationFreeSpace(stateDir)
	if err != nil {
		return fmt.Errorf("measure free space for the state backup: %w", err)
	}
	if free < need {
		return fmt.Errorf("state backup before migration v%d needs %s free; free some space and start again",
			stateSchemaVersion, formatBytesV11(need))
	}
```

### 既有测试钉住的东西（`pkg/state/schema_migrations_test.go`）

- `:2402-2406`：把 `migrationFreeSpace` 换成返回 1 的函数，断言 `Open` 失败且错误信息含 **`free some space and start again`**（新信息必须保留这个子串），并断言库未被触碰（`user_version` 仍是 10、无 `backups/` 目录）。
- `var migrationFreeSpace = diskFreeSpace`（`schema_migrations.go:1451`）是仓库既有的测试注入先例。

## Commands you will need

| Purpose | Command | Expected on success |
|---|---|---|
| 编译 | `CGO_ENABLED=1 go build ./...` | exit 0 |
| 本包测试 | `CGO_ENABLED=1 go test -tags fts5 -count=1 ./pkg/state` | `ok`（基线约 21s） |
| 预检回归 | `CGO_ENABLED=1 go test -tags fts5 -count=1 ./pkg/state -run TestMigrateAwayStoredStreamDeltas -v` | 3 个子测试全 PASS |
| vet / gofmt | `CGO_ENABLED=1 go vet -tags fts5 ./pkg/state/...`、`gofmt -l pkg/state` | 无输出 |

## Scope

**In scope**（只改这两个文件）：
- `pkg/state/schema_migrations.go`
- `pkg/state/schema_migrations_test.go`（只在需要补断言时；见 Step 4）
- `plans/README.md`（只改 049 的状态行）

**Out of scope**（一律不许动）：
- `pkg/state/db.go` 的 DSN：不给普通连接设 `_cache_size` / WAL 限额（运行期的高水位 WAL 复用是有意的；本计划只约束迁移这一次）。
- `temp_store` / `mmap_size`：**不要**把 `temp_store` 设为 MEMORY——大 GROUP BY 的排序会转成内存驻留，可能吃掉数 GB；`mmap_size` 在不同构建下上限不同（amalgamation 里既有 `#define SQLITE_MAX_MMAP_SIZE 0` 也有 `0x7fff0000` 的分支），本期不引入。
- 尾部 `VACUUM` 与备份保留：`plans/050` 负责。
- 任何迁移语句、`stateSchemaVersion`、`schema.sql`。

## Git workflow

- 不建分支、不 commit、不 push；改动留在工作区。
- 若将来提交，建议 message：`perf(state): size the migration connection for the work, and preflight its WAL`。

## Steps

### Step 1: 基线与漂移检查

```bash
git diff --stat a8140fc..HEAD -- pkg/state/schema_migrations.go
CGO_ENABLED=1 go test -tags fts5 -count=1 ./pkg/state
grep -n "PRAGMA busy_timeout = 600000" pkg/state/schema_migrations.go        # 1 行
grep -n "need := int64(float64(info.Size()) \* 1.1)" pkg/state/schema_migrations.go   # 1 行
```

**Verify**: 测试 `ok`；两条 grep 各命中 1 行（若 048 已落地，`schema_migrations.go` 的 diff 只应出现在 048 的区域内）。

### Step 2: 迁移连接的页缓存与 WAL 限额

在 `PRAGMA busy_timeout = 600000` 的 defer 之后、`PRAGMA foreign_keys = OFF` 之前插入：

```go
	// The migration is one transaction over a multi-gigabyte file: the purge
	// re-reads pages it has already visited and the tool-output step sorts
	// spill, so the driver's compiled 2MiB default page cache is the wrong
	// size for this work. The setting is per connection and the pool hands
	// this connection to ordinary callers next, so it is restored with the
	// same deferred pattern as busy_timeout.
	if _, err := conn.ExecContext(ctx, `PRAGMA cache_size = -131072`); err != nil {
		return err
	}
	defer func() {
		if _, pragmaErr := conn.ExecContext(context.Background(), `PRAGMA cache_size = -2000`); err == nil && pragmaErr != nil {
			err = pragmaErr
		}
	}()
	// The transaction leaves a write-ahead log about as large as the pages it
	// rewrote. With the default -1 (no limit) that file keeps its high-water
	// mark for the life of the database — a real library was measured holding
	// a 3.1GB -wal beside a 5.6GB database. The limit makes the checkpoint
	// that follows the commit truncate it once; it is restored afterwards so
	// ordinary runtime keeps the default.
	if _, err := conn.ExecContext(ctx, `PRAGMA journal_size_limit = 67108864`); err != nil {
		return err
	}
	defer func() {
		if _, pragmaErr := conn.ExecContext(context.Background(), `PRAGMA journal_size_limit = -1`); err == nil && pragmaErr != nil {
			err = pragmaErr
		}
	}()
```

要点（都为「为什么」服务，执行者不要删注释）：
- `cache_size` 为负值表示 KiB：`-131072` = 128 MiB，有界、按需分配。
- 还原值是驱动的编译默认 `-2000`（2 MiB），不是「随便一个数」；`journal_size_limit` 的还原值是 `-1`。
- 两个 defer 都在 `COMMIT` 之后才执行（`migrateStateSchema` 的提交发生在函数中部），所以截断发生在限额生效期间。

**Verify**:
```bash
grep -c "PRAGMA cache_size = -131072\|PRAGMA journal_size_limit = 67108864" pkg/state/schema_migrations.go   # 2
grep -c "PRAGMA cache_size = -2000\|PRAGMA journal_size_limit = -1" pkg/state/schema_migrations.go           # 2
CGO_ENABLED=1 go test -tags fts5 -count=1 ./pkg/state
```
→ 测试仍 `ok`（这两个 PRAGMA 对既有测试的库大小无可见影响）。

### Step 3: 预检按「备份 + WAL」计算

把 `backupBeforeStreamDeltaMigrationV11` 里的 `need` 计算与错误信息换成：

```go
	// The migration holds one transaction over the whole purge and writes a
	// WAL frame for every page it dirties, on the same filesystem as the
	// database. The free space therefore has to cover two things of roughly
	// the file's own size: the VACUUM INTO copy this function writes, and the
	// write-ahead log the migration grows beside it. The trailing VACUUM's
	// temporary file is bounded by the compacted database, which is smaller
	// than both, so 2.2x is the honest number — refusing now beats aborting
	// and rolling back after the purge has run for minutes.
	need := int64(float64(info.Size()) * 2.2)
	stateDir := filepath.Dir(dbPath)
	free, err := migrationFreeSpace(stateDir)
	if err != nil {
		return fmt.Errorf("measure free space for the state backup: %w", err)
	}
	slog.Info("state migration space check",
		"database_bytes", info.Size(), "free_bytes", free, "need_bytes", need)
	if free < need {
		return fmt.Errorf("state migration to v%d needs %s free for its backup and write-ahead log; free some space and start again",
			stateSchemaVersion, formatBytesV11(need))
	}
```

注意：错误信息必须**保留** `free some space and start again`（`schema_migrations_test.go:2406` 断言这个子串）。

**Verify**:
```bash
CGO_ENABLED=1 go test -tags fts5 -count=1 ./pkg/state -run TestMigrateAwayStoredStreamDeltas -v
grep -n "free some space and start again" pkg/state/schema_migrations.go | wc -l   # 1
```
→ 三个子测试全 PASS（空间检查子测试正是靠 `migrationFreeSpace` 注入 1 字节触发的）。

### Step 4: 预检数字的护栏测试（测试文件）

在 `schema_migrations_test.go` 里给空间检查子测试补一条断言：预检必须**包含 WAL 的份量**，而不是只有副本。做法是在同一个子测试里把 `migrationFreeSpace` 依次设成两个值：

- `free = fileSize × 1.2`（够 1.1× 副本、不够「副本 + WAL」）→ **必须拒绝**（修复前会放行，这就是回归点）；
- `free = fileSize × 2.5` → 放行到下一步（备份真的会被写出来；测试随后清理临时目录即可）。

`fileSize` 用 `os.Stat(path)` 取。第二个值仍会走完整的 `VACUUM INTO`，夹具很小，开销可接受。

**Verify**: `CGO_ENABLED=1 go test -tags fts5 -count=1 ./pkg/state -run TestMigrateAwayStoredStreamDeltas -v` → 全 PASS；且把 Step 3 的 `2.2` 临时改回 `1.1` 时，`fileSize × 1.2` 那一档应当 FAIL（报告里附这条对照输出，然后改回 `2.2`）。

### Step 5: 真机观察（可选，但建议做并写进报告）

在 owner 的库上不做写操作。可选的做法是在 `/tmp` 造一个 `schemaSQL` + 若干行的 v10 库，跑一次迁移，然后：

```bash
ls -l /tmp/<dir>/state.sqlite*        # 迁移后 -wal 应被截断（远小于迁移期间的大小）
sqlite3 /tmp/<dir>/state.sqlite "PRAGMA journal_size_limit;"   # 若在迁移连接上读，应为 67108864
```

**注意**：截断需要一次**完整**的 checkpoint；如果另一个连接正在读，checkpoint 会退化为部分、`-wal` 不会缩小。这是既有行为，不要为了它改代码；若观察到没有缩小，把 `ls -l` 输出贴进报告即可（说明当时有并发读者）。

## Test plan

- 修改：空间检查子测试补两档注入（1.2× 必须拒绝、2.5× 放行），见 Step 4。
- 既有断言原样保留：`free some space and start again` 子串、`user_version` 未被触碰、被拒绝时不留下 `backups/`、迁移后备份目录恰好 1 个文件。
- 验证：`CGO_ENABLED=1 go test -tags fts5 -count=1 ./pkg/state` → `ok`。

## Done criteria

ALL must hold:

- [ ] `CGO_ENABLED=1 go test -tags fts5 -count=1 ./pkg/state` → `ok`
- [ ] `grep -c "PRAGMA cache_size = -131072" pkg/state/schema_migrations.go` → `1`；`grep -c "PRAGMA journal_size_limit = 67108864"` → `1`
- [ ] `grep -n "float64(info.Size()) \* 1.1" pkg/state/schema_migrations.go` → 无输出
- [ ] `grep -n "free some space and start again" pkg/state/schema_migrations.go` → 恰好 1 行（且信息里点名 backup 与 write-ahead log）
- [ ] Step 4 的 1.2× 对照（把 2.2 改回 1.1 时失败）已贴进报告并改回
- [ ] `CGO_ENABLED=1 go vet -tags fts5 ./pkg/state/...` 与 `gofmt -l pkg/state` 无输出
- [ ] `git status --short` 只多出 `pkg/state/schema_migrations.go`、`pkg/state/schema_migrations_test.go`、`plans/README.md`
- [ ] `plans/README.md` 中 049 的状态行更新

## STOP conditions

Stop and report back (do not improvise) if:

- `PRAGMA cache_size = -131072` 或 `PRAGMA journal_size_limit = ...` 返回错误（驱动/构建不支持），或任何既有测试因此改变行为。
- 空间检查子测试在 2.5× 档仍被拒绝（说明 `migrationFreeSpace` 的注入路径或 `info.Size()` 的前提与计划设想不同）。
- 为了让 1.2× 档通过而需要改 `diskFreeSpace` / `migrationFreeSpace` 的实现（本计划只允许在测试里替换它）。
- 预检数字需要「按版本分档」才能让既有测试通过——停下报告（说明更简单的统一 2.2× 不成立）。
- 需要触碰 Out of scope 的 `pkg/state/db.go` DSN 才能见效。

## Maintenance notes

- **不变式**：迁移连接上的每一个「提升」都必须有配对的 defer 还原，且还原值是 DSN/编译默认（`busy_timeout` → `stateBusyTimeoutMs`、`cache_size` → `-2000`、`journal_size_limit` → `-1`）。新增设置时照此办理。
- **`need = 2.2 × fileSize` 的含义**：1.1× 给 `VACUUM INTO` 副本（压缩后可能接近原大小）、1× 留给迁移事务的 WAL。若将来把迁移拆成多个事务（见 048 的维护说明），第二项可以缩小——那时再回来改这个数字与它的注释。
- **审阅重点**：两个 defer 的还原值是否与注释一致；`slog.Info` 的空间快照是否在拒绝分支之前（无论放行还是拒绝都要留下数字）；错误信息是否仍被既有测试的子串断言覆盖。
- **明确不做**：VACUUM INTO 替换尾部原地 VACUUM（已评估并拒绝，原因见 `plans/README.md`）；普通连接设 `journal_size_limit`（运行期保留 WAL 高水位是有意的）。
