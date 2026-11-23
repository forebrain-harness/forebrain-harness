# Plan 050: 只有回收量可观时才重写整库；同一来源的旧备份迁移成功后只留最新一份

> **REJECTED（2026-10-14 复评，不要执行）**：迁移自己跑完的实测数据显示两项都失去标的——(1) `freed_pages=961977`（≈3.9 GB）那次回收，从 v11 结束到 v14 完成一共只用 **54 秒**（15:05:15 → 15:06:09），阈值只为未来的小步升级省下「1.6 GB 文件重写」的十几秒；(2) owner 已手动删除那份 5.6 GB 备份，`backups/` 当前不存在，且备份函数有 `version >= stateSchemaVersion` 的门（v11 之后不再写新备份）——保护的是一组不会再增长的文件。理由与数据见 `plans/README.md` 顶部复评。
>
> **Executor instructions**: Follow this plan step by step. Run every
> verification command and confirm the expected result before moving to the
> next step. If anything in the "STOP conditions" section occurs, stop and
> report — do not improvise. When done, update the status row for this plan
> in `plans/README.md`.
>
> 本计划把「迁移收尾」的两件事从「无条件」改成「有阈值、有保留策略」：尾部 VACUUM 只在回收量值得一次整库重写时跑；`backups/` 里同一来源版本的旧副本在迁移成功后清理，只留最新一份。**清理的语义与既有断言（迁移后不再有可回收页、字节级结果不变）必须保持**。
>
> **Drift check (run first)**: `git diff --stat a8140fc..HEAD -- pkg/state/schema_migrations.go pkg/state/schema_migrations_test.go`
> `plans/048` / `plans/049` 会改同一批文件（048 在清理语句、049 在 PRAGMA 与预检）。先把 "Current state" 的摘录与实码对照；本计划的落点在 `migrateStateSchema` 尾部与 `backupBeforeStreamDeltaMigrationV11` 尾部，与 048 不重叠、与 049 相邻（049 改预检的 `need` 计算，本计划改它之后的备份文件名与清理）——对不上才按 STOP 处理。

## Status

- **Priority**: P2
- **Effort**: S–M（约 120 行含注释、一个清理 `func` 与两类测试）
- **Risk**: LOW–MED（VACUUM 阈值只影响「要不要回收」，不改数据；备份清理是删文件，规则必须严格按文件名模式匹配，且绝不碰迁移刚写出的那一份）
- **Depends on**: none（建议在 048、049 之后执行，避免同文件合并噪音）
- **Category**: perf / migration
- **Planned at**: commit `a8140fc`, 2026-10-14

## Why this matters

两处「无条件」的收尾动作，在一个 5.6 GB 的真实库上都变成了浪费或沉默的磁盘债：

1. **尾部 VACUUM 的触发条件是 `freelist_count > 0`——任何迁移只要释放过一页，就要重写整库。** `migrateStateV13ToV14`（`pkg/state/schema_migrations.go:666-690`）会把小表 `fb_cron_jobs` rename/copy/drop 一遍，即便它只有几行，drop 也会释放它的根页；于是「v13→v14 的一次 ADD COLUMN 级升级」会触发对 5.6 GB 文件的完整重写（读全库 + 写压缩副本 + 拷回），并在整段时间里占着写锁（另一个进程最多要等 600s 的迁移 busy_timeout）。反过来，v10→v11 清理释放的是**约 98% 的行**（实测库：7.69M 行里 7.57M 是流式增量）——这种回收必须做。缺的不是「要不要回收」，而是「回收量值不值」。
2. **预迁移备份只增不减。** 每次尝试都会 `VACUUM INTO` 一份与库同尺寸的副本（实测 5.6 GB），文件名带时间戳所以永不冲突；失败/中断后重跑再写一份。仓库里没有任何清理代码（`grep -rn "backups" pkg/state/*.go` 只命中注释与 `MkdirAll`），用户也不知道它在那里、可以删。owner 机器上 16 GiB 可用空间里，一份 5.6 GB 的副本就是三分之一。

落地后：VACUUM 只在回收量 ≥ 64 MiB **或** ≥ 页数的 10% 时跑（并记录跳过时的数字，别默默不做事）；迁移成功后 `backups/` 里同一来源版本只留最新一份，其余删除并逐条记日志。

## Current state

### 尾部收尾（`pkg/state/schema_migrations.go:1652-1668`，全文）

```go
	// The rename-aside/copy/drop rebuild leaves the file full of free pages;
	// hand them back once nothing holds a transaction on this database. A step
	// that only added something freed nothing, and rewriting an 800MB file
	// for it would hold every other process off the database for no gain.
	var freePages int64
	if err := conn.QueryRowContext(ctx, `PRAGMA freelist_count`).Scan(&freePages); err != nil {
		return err
	}
	if freePages > 0 {
		if _, err := conn.ExecContext(ctx, `VACUUM`); err != nil {
			return fmt.Errorf("vacuum migrated state database: %w", err)
		}
	}
	slog.Info("migrated state database", "from_version", from, "to_version", stateSchemaVersion, "freed_pages", freePages)
	return nil
```

注意这段注释的最后一层意思——「为一个只加过列的步骤重写 800MB 文件不值得」——**代码并没有实现它**，只实现了 `freePages > 0`。本计划就是用阈值把那句话落地。

### 备份的写出与命名（`pkg/state/schema_migrations.go:1503-1514`）

```go
	backupDir := filepath.Join(stateDir, "backups")
	if err := os.MkdirAll(backupDir, 0o700); err != nil {
		return fmt.Errorf("create the state backup directory: %w", err)
	}
	backupPath := filepath.Join(backupDir, fmt.Sprintf("forebrain.state.v%d.%s.sqlite",
		version, time.Now().UTC().Format("20060102T150405.000000000Z")))
	if _, err := conn.ExecContext(ctx, `VACUUM INTO ?`, backupPath); err != nil {
		return fmt.Errorf("copy the state database to %s: %w", backupPath, err)
	}
	slog.Info("backed up the state database before migrating",
		"from_version", version, "to_version", stateSchemaVersion, "path", backupPath)
	return nil
```

时间戳格式 `20060102T150405.000000000Z` 让文件名**按字典序 = 按时间序**，所以「最新一份」= 同前缀里字典序最大者。

### 既有测试钉住的东西（`pkg/state/schema_migrations_test.go`）

- `:2372-2379`：迁移后 `PRAGMA freelist_count` 必须为 0（注释：「the VACUUM ran」）。夹具的批量行是 `streamDeltaBulkV11 = 400`（`:2211`），释放量在百 KB 量级——**默认 64 MiB 阈值下会跳过 VACUUM**，所以这个子测试必须显式把阈值调成 0（见 Step 3）。
- `:2382-2398`：备份目录**恰好 1 个文件**，且该文件能被打开、里面保有迁移前的全部增量行。保留策略「同版本只留最新」与该断言相容。
- 测试注入变量的先例：`var migrationFreeSpace = diskFreeSpace`（`schema_migrations.go:1451`）。

### 仓库约定

- 注释写完整英文句子、解释「为什么」；日志用 `slog.Info`，字段名小写下划线。
- 迁移里凡是「可能什么都不做」的路径都要有日志（例如已存在的 pragma restore 分支），本计划的跳过分支照此办理。
- 新增的包级变量必须是「为测试注入」或「为常量命名」二者之一，并写清是哪一种。

## Commands you will need

| Purpose | Command | Expected on success |
|---|---|---|
| 编译 | `CGO_ENABLED=1 go build ./...` | exit 0 |
| 本包测试 | `CGO_ENABLED=1 go test -tags fts5 -count=1 ./pkg/state` | `ok`（基线约 21s） |
| 收尾回归 | `CGO_ENABLED=1 go test -tags fts5 -count=1 ./pkg/state -run 'TestMigrateAwayStoredStreamDeltas\|TestPruneStateBackups\|TestV11SmallReclaim' -v` | 全 PASS |
| vet / gofmt | `CGO_ENABLED=1 go vet -tags fts5 ./pkg/state/...`、`gofmt -l pkg/state` | 无输出 |

## Scope

**In scope**（只改这两个文件）：
- `pkg/state/schema_migrations.go`
- `pkg/state/schema_migrations_test.go`
- `plans/README.md`（只改 050 的状态行）

**Out of scope**（一律不许动）：
- **替换原地 VACUUM 为 VACUUM INTO + 文件替换**：已评估并拒绝（见 `plans/README.md`）。原因两条：实测 WAL 模式下原地 VACUUM 不会撑大 WAL（源码路径 `sqlite3RunVacuum` → `sqlite3BtreeCopyFile` 整页拷回，不走 WAL 追加），而且换文件会绕过 SQLite 自己的锁协调——迁移特意用一个 `BEGIN IMMEDIATE` 事务让其他进程等到提交后看到新版本，换 inode 会让正在等待的进程看向旧文件。
- `PRAGMA foreign_key_check` 的按表收窄：已评估并拒绝（它是这套重建类迁移的安全闸门，按表收窄需要 14 个迁移各自申报「动过哪些表」，收益不抵风险）。
- `auto_vacuum=INCREMENTAL`：不在本计划（它要求在建库/首次 VACUUM 前设定，牵扯 schema.sql 与全新库路径，单独立项）。
- `pkg/state/db.go`、`schema.sql`、`stateSchemaVersion`、任何迁移语句。
- 备份目录里**不匹配模式**的文件（用户的其它东西）——一个都不许删。

## Git workflow

- 不建分支、不 commit、不 push；改动留在工作区。
- 若将来提交，建议 message：`perf(state): reclaim only when it is worth a rewrite, and keep one backup per source version`。

## Steps

### Step 1: 基线与漂移检查

```bash
CGO_ENABLED=1 go test -tags fts5 -count=1 ./pkg/state
grep -n "if freePages > 0 {" pkg/state/schema_migrations.go                    # 1 行
grep -rn "backups" pkg/state/*.go | grep -v _test.go                           # 只有注释与 MkdirAll
```

**Verify**: 测试 `ok`；两条 grep 结果如上。

### Step 2: VACUUM 阈值

**2a.** 在 `migrateStateSchema` 之前（靠近 `var migrationFreeSpace = ...`，`:1451` 一带）新增：

```go
// migrationVacuumMinReclaim is how many bytes of free pages a migration must
// leave behind before the pipeline rewrites the whole file to hand them back.
// A step that freed the root page of a small table is not worth a rewrite of
// a multi-gigabyte database: the pages are reused by later writes anyway. The
// counterpart is deliberate too — the v10→v11 purge frees most of a large
// library, and that reclaim must still happen. A variable so a test can pin
// both the reclaim and the skip path.
var migrationVacuumMinReclaim = int64(64 << 20)
```

**2b.** 把尾部收尾块换成（保留原注释的前两句，第三句现在真的成立了）：

```go
	// The rename-aside/copy/drop rebuild leaves the file full of free pages;
	// hand them back once nothing holds a transaction on this database. A step
	// that only added something freed nothing, and rewriting an 800MB file
	// for it would hold every other process off the database for no gain —
	// so an add-column-sized reclaim is left where it is, and the log says so.
	var freePages, pageCount, pageSize int64
	for _, q := range []struct {
		sql  string
		dest *int64
	}{
		{`PRAGMA freelist_count`, &freePages},
		{`PRAGMA page_count`, &pageCount},
		{`PRAGMA page_size`, &pageSize},
	} {
		if err := conn.QueryRowContext(ctx, q.sql).Scan(q.dest); err != nil {
			return err
		}
	}
	reclaim := freePages * pageSize
	worthRewriting := pageCount > 0 && freePages*100 >= pageCount*10
	if freePages > 0 && (reclaim >= migrationVacuumMinReclaim || worthRewriting) {
		if _, err := conn.ExecContext(ctx, `VACUUM`); err != nil {
			return fmt.Errorf("vacuum migrated state database: %w", err)
		}
	} else if freePages > 0 {
		slog.Info("state migration left free pages in place",
			"free_pages", freePages, "reclaimable_bytes", reclaim, "page_count", pageCount)
	}
	slog.Info("migrated state database", "from_version", from, "to_version", stateSchemaVersion, "freed_pages", freePages)
	return nil
```

注意 `freePages > 0 &&` 这个前缀**不能省**：阈值为 0 时（测试注入）`0 >= 0` 会为真，不能因此对一个没有可回收页的库跑 VACUUM。

**Verify**:
```bash
grep -n "migrationVacuumMinReclaim" pkg/state/schema_migrations.go | wc -l   # ≥ 2（定义 + 使用）
grep -n "if freePages > 0 && (reclaim >= migrationVacuumMinReclaim" pkg/state/schema_migrations.go  # 1 行
CGO_ENABLED=1 go test -tags fts5 -count=1 ./pkg/state -run TestMigrateAwayStoredStreamDeltas -v
```
→ 最后一条此时应当**失败**在 `freelist_count after migration = N, want 0 (the VACUUM ran)`——因为夹具的回收量只有百 KB 级。这是预期，Step 3 会把它修好（在子测试里注入阈值 0）。**不要**为了让它过而把默认阈值改小。

### Step 3: 既有测试注入阈值 + 新增「小回收不重写」测试

**3a.** 在 `TestMigrateAwayStoredStreamDeltas` 的第一个子测试里（`streamDeltaFixtureV11` 之后、`Open` 之前）加入：

```go
		// The bulk fixture frees kilobytes, which the shipped threshold
		// deliberately ignores; this subtest pins the purge's outcome, so it
		// asks for the reclaim explicitly.
		oldReclaim := migrationVacuumMinReclaim
		migrationVacuumMinReclaim = 0
		defer func() { migrationVacuumMinReclaim = oldReclaim }()
```

**3b.** 新增一个子测试（或独立测试）钉住跳过路径。夹具形状：`sqlite.Open` + `db.Exec(schemaSQL)`，建一张小表、插 ~200 行、`DROP TABLE`（这一步留下几页 freelist），`PRAGMA user_version = 13`，关闭。然后：

```go
	t.Run("a small reclaim does not rewrite the file", func(t *testing.T) {
		// path 如上构造；记录 os.Stat(path).Size() 与 freelist_count（> 0）
		// Open(...) 迁移（v13→v14 在 schemaSQL 形状下会走 guard 提前返回，不释放新页）
		// 断言：迁移成功（requireSchemaVersion == stateSchemaVersion）、freelist_count 仍 > 0、
		//       且 os.Stat 的文件大小与迁移前**相同**（文件没有被重写）。
	})
```

**Verify**: `CGO_ENABLED=1 go test -tags fts5 -count=1 ./pkg/state -run 'TestMigrateAwayStoredStreamDeltas' -v` → 全 PASS（含新子测试）。

### Step 4: 备份保留

**4a.** 在 `backupBeforeStreamDeltaMigrationV11` 附近新增两个符号：

```go
// stateBackupNameV11 reports whether name is one of the pre-migration backups
// this package writes, and its source version. The pattern is anchored so a
// user's own file in the directory can never match.
var stateBackupNameV11 = regexp.MustCompile(`^forebrain\.state\.v(\d+)\.\d{8}T\d{6}\.\d{9}Z\.sqlite$`)

// pruneStateBackupsV11 keeps the newest backup per source version and removes
// the older ones. The pre-migration copy is insurance for one upgrade, not an
// archive: it is as large as the database it copied, it is written again on
// every attempt, and the timestamp in its name sorts lexicographically, so
// "newest" needs no stat. Files that do not match the name pattern are left
// alone, and so is an empty or missing directory.
func pruneStateBackupsV11(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	newest := map[string]string{}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		m := stateBackupNameV11.FindStringSubmatch(e.Name())
		if m == nil {
			continue
		}
		if keep, ok := newest[m[1]]; !ok || e.Name() > keep {
			newest[m[1]] = e.Name()
		}
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		m := stateBackupNameV11.FindStringSubmatch(e.Name())
		if m == nil || newest[m[1]] == e.Name() {
			continue
		}
		if err := os.Remove(filepath.Join(dir, e.Name())); err != nil {
			return err
		}
		slog.Info("state migration removed an older backup", "path", filepath.Join(dir, e.Name()), "kept", newest[m[1]])
	}
	return nil
}
```

需要在导入块加 `regexp`（当前没有）。

**4b.** 在 `migrateStateSchema` 里，**迁移成功之后**（`slog.Info("migrated state database", ...)` 之前）调用它；失败只 warning，不影响迁移结果——平台在此时已经可用，清理是家务：

```go
	if err := pruneStateBackupsV11(filepath.Join(filepath.Dir(dbPath), "backups")); err != nil {
		slog.Warn("state migration could not prune old backups", "error", err)
	}
```

**Verify**:
```bash
grep -n "regexp.MustCompile" pkg/state/schema_migrations.go     # 1 行
CGO_ENABLED=1 go test -tags fts5 -count=1 ./pkg/state
```

### Step 5: 备份保留的测试

新增 `TestPruneStateBackupsV11KeepsTheNewestPerVersion`：在 `t.TempDir()` 里造目录并写入**空文件**（内容无关，测的是名字规则）：

| 文件 | 期望 |
|---|---|
| `forebrain.state.v10.20260101T000000.000000000Z.sqlite` | 删除（同版本更旧） |
| `forebrain.state.v10.20261014T053111.088539000Z.sqlite` | 保留（v10 最新） |
| `forebrain.state.v9.20260101T000000.000000000Z.sqlite` | 保留（另一个来源版本） |
| `forebrain.state.v10.notatimestamp.sqlite` | 保留（不匹配模式） |
| `notes.txt` | 保留（不匹配模式） |
| `sub/`（目录） | 保留（目录不处理） |

断言：调用 `pruneStateBackupsV11(dir)` 后目录内容与上表一致；再调一次是幂等的（不报错、不再删）。另外补一条：目录不存在时返回 nil。

**Verify**: `CGO_ENABLED=1 go test -tags fts5 -count=1 ./pkg/state -run TestPruneStateBackupsV11KeepsTheNewestPerVersion -v` → PASS。

### Step 6: 端到端确认备份只剩最新一份

在既有 `TestMigrateAwayStoredStreamDeltas` 的「备份目录恰好 1 个文件」断言之前，**先**在同目录放一个更旧的同版本备份（用与 fixture 相同的命名模式，例如 `v10.20200101T000000.000000000Z.sqlite`），然后断言迁移后目录里仍恰好 1 个文件、且它是本次迁移写出的那一个（名字里的时间戳最大）。

**Verify**: `CGO_ENABLED=1 go test -tags fts5 -count=1 ./pkg/state -run TestMigrateAwayStoredStreamDeltas -v` → 全 PASS。

## Test plan

- 新增：`TestV11SmallReclaimDoesNotRewrite`（名字自定，见 Step 3b；断言文件大小未变 + freelist 仍 > 0）、`TestPruneStateBackupsV11KeepsTheNewestPerVersion`（Step 5 的表）。
- 修改：`TestMigrateAwayStoredStreamDeltas`（注入阈值 0；备份目录先放一份更旧的同版本副本，断言迁移后只剩最新一份）。
- 结构范本：`streamDeltaFixtureV11`（`schema_migrations_test.go:2116`）的建夹具手法（`sql.Open(sqliteDriverName(), path)` + `db.Exec(schemaSQL)` + 末尾设 `user_version`）。
- 验证：`CGO_ENABLED=1 go test -tags fts5 -count=1 ./pkg/state` → `ok`。

## Done criteria

ALL must hold:

- [ ] `CGO_ENABLED=1 go test -tags fts5 -count=1 ./pkg/state` → `ok`
- [ ] `grep -c "migrationVacuumMinReclaim" pkg/state/schema_migrations.go` → `≥ 2`；`grep -c "pruneStateBackupsV11"` → `≥ 2`
- [ ] `grep -n "if freePages > 0 {" pkg/state/schema_migrations.go` → 无输出（无条件 VACUUM 已消失）
- [ ] 新测试 `TestV11SmallReclaim*` 与 `TestPruneStateBackupsV11KeepsTheNewestPerVersion` 存在且 PASS
- [ ] 「小回收不重写」测试断言了**文件大小不变**（不只是 freelist），并把迁移前后的 `os.Stat` 数字写进报告
- [ ] `CGO_ENABLED=1 go vet -tags fts5 ./pkg/state/...` 与 `gofmt -l pkg/state` 无输出
- [ ] `git status --short` 只多出 `pkg/state/schema_migrations.go`、`pkg/state/schema_migrations_test.go`、`plans/README.md`
- [ ] `plans/README.md` 中 050 的状态行更新

## STOP conditions

Stop and report back (do not improvise) if:

- 「小回收不重写」的夹具在默认阈值下 **freelist 仍为 0**（说明那个迁移路径其实没有释放页，测试失去了意义）——换一个真的会释放页的夹具形状，或停下报告。
- v13→v14 在该夹具上**没有**提前返回（即它真的做了表重建），导致无法构造「小回收」场景——停下报告，附上 `PRAGMA table_info(fb_cron_jobs)` 与 fixture 的 `user_version` 现状。
- 备份保留的测试需要改模式正则才能通过（例如时间戳格式与 `time.Format` 的实际输出不符）——报告实际文件名，不要放宽正则去迁就一个错误假设。
- 任何既有断言（迁移结果、备份可打开、`freelist_count == 0` 的那条在阈值注入下）必须改语义才能通过。
- 需要触碰 Out of scope 的文件或引入 `auto_vacuum`。

## Maintenance notes

- **不变式**：阈值是「两档或」——绝对量（64 MiB）或相对量（页数 10%）。改动其中一档时，另一档的语义要一并复核；如果将来 v11 的清理被移出迁移（例如改成后台任务），这里的两档都需要重新标定。
- **备份保留与并发**：清理发生在迁移提交之后。若同一目录下另一进程正在读某份备份（用户手动恢复），`os.Remove` 在 unix 上会成功而读方仍持有 fd；这是可接受的。但**不要**把清理挪到迁移开始之前。
- **审阅重点**：`stateBackupNameV11` 正则是否与 `fmt.Sprintf("forebrain.state.v%d.%s.sqlite", version, time.Now().UTC().Format("20060102T150405.000000000Z"))` 的实际输出逐段对应（大写 T、纳秒 9 位、`Z` 结尾）；`freePages > 0 &&` 前缀是否在；跳过分支是否留下了可诊断的日志。
- **明确不做**：VACUUM INTO 文件替换（拒绝理由见 Scope）；`foreign_key_check` 按表收窄（拒绝理由见 Scope）；`auto_vacuum=INCREMENTAL`（单独立项）。
