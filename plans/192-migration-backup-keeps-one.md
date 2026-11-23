# 192 — 迁移前备份只留最近 1 份：新备份落地后清掉更早的

- **基线**：`6f68cb21`（dev）。写计划时未落地任何本计划改动。
- **状态**：已执行（2026-10-30 exec/192 worktree，commit `bc3339b7`，评审 APPROVE，已 ff-merge 落 dev，worktree/分支已删）
- **依赖**：无。与计划族 187–191（配置入库）互不依赖，可插队执行；187 的 schema v22 迁移会自然触发本计划的清理路径，顺带清掉 owner 机器上积压的 3 份旧备份。

## 0. 背景（为什么做这件事）

`<home>/state/backups/` 是状态库**迁移前的自动全量备份**（`backupBeforeStreamDeltaMigrationV11`，`pkg/state/schema_migrations.go:1726-1778`）：库版本落后且仍有未合并 stream-delta 事件时，`VACUUM INTO` 把整库复制成 `backups/forebrain.state.v<旧版本>.<UTC时间戳>.sqlite`。**现状没有任何清理逻辑**，每次触发迁移的升版都再拷一份全库、只增不减——owner 机器已积 3 份共约 7.7 GB（v14 1.8G / v15 2.5G / v16 3.3G）。

owner 裁决（2026-10-30，在配置入库计划族讨论中）：**保留该功能，但只留最近 1 份**——新备份成功落地后，删除同目录更早的备份。

## 1. 现状事实（已对基线逐条核实）

| # | 事实 | 位置 |
|---|---|---|
| F1 | 备份函数：版本检查（< `stateSchemaVersion`）→ `fb_session_events` 存在 → `streamDeltaTypesV11` 待合并事件 > 0 → 空间检查（≥ 库体积×1.1，不足则**拒绝迁移**）→ `MkdirAll(backups, 0700)` → `VACUUM INTO backups/forebrain.state.v<from>.<ts>.sqlite` → slog Info | `pkg/state/schema_migrations.go:1726-1778` |
| F2 | 唯一调用点：迁移框架里、`BEGIN IMMEDIATE` 事务之前（VACUUM 不能进事务） | `schema_migrations.go:1846-1848` |
| F3 | 全仓无任何删除 backups 文件的代码（非测试） | grep `backups` 仅命中 `:1709,1766` |
| F4 | 既有测试：主用例断言单次迁移后 backups 目录**恰好 1 个文件**且内容完整（open + 数 delta）；空间不足用例断言拒绝且不建目录；`streamDeltaFixtureV11(t, version)` 是现成 fixture 构造器 | `pkg/state/schema_migrations_test.go:2760-2807` |
| F5 | 备份文件名＝`forebrain.state.v<数字>.<20060102T150405.000000000Z>.sqlite`；同目录可能存在的其他文件不属于本功能 | `schema_migrations.go:1770-1771` |

## 2. 设计

**改动一处**：`backupBeforeStreamDeltaMigrationV11` 在 `VACUUM INTO` 成功、slog Info 之后追加清理步骤：

1. 读 `backups` 目录（`os.ReadDir`；目录刚建/刚写过，读失败属异常 → 归入 best-effort 语义，见下）。
2. 过滤：只匹配本功能的命名——文件名以 `forebrain.state.v` 开头且以 `.sqlite` 结尾（对齐 F5；**不匹配的文件一律不动**——用户可能把自己的恢复副本放在同目录）。
3. 删除：匹配文件中**除刚写入的 `backupPath` 外全部删除**（刚写入的按定义就是最新；旧份对应的是早已成功完成的迁移，不再有保留价值）。
4. **best-effort**：单个删除失败（`os.Remove` 返回 err）→ `slog.Warn` 记文件名与错误，继续删其余；**绝不让清理失败升级为备份/迁移失败**——备份本体已成功，清理只是腾空间。
5. 语义边界：**只在成功创建新备份时清理**。不新增启动时清理、不加配置开关（owner 裁决是固定策略）、不改备份的触发条件（F1 的四道闸原样）。若某次升版因 pending==0 未触发备份（F1 条件不满足），旧份原样保留到下一次触发——可接受，不加额外钩子。
6. 清理结果计入既有 slog Info 行（追加 ` "kept", backupPath, "removed", n`）——一行日志说清保留/删除了什么。

**对 owner 存量 3 份的处理**：无专门迁移。计划 187 的 v22 迁移触发备份后（owner 的 v14/v15/v16 三连份说明 pending 条件几乎每次都满足），旧 3 份被本清理一次删掉，目录回到 1 份。

## 3. 步骤

1. **实现**：`schema_migrations.go` 新增私有函数 `pruneOldStateBackups(backupDir, keepPath string)`（匹配+删除+Warn，返回删除数）；`backupBeforeStreamDeltaMigrationV11` 在 `:1775` slog Info 前后接入。
   验证：`go build ./pkg/state`。
2. **测试**（`pkg/state/schema_migrations_test.go`，沿用 F4 的既有用例风格）：
   - 新子用例「只留最新一份」：`streamDeltaFixtureV11` 建库 → 在 backups 目录**预放**两个旧命名文件（`forebrain.state.v10.<旧时间戳>.sqlite`、`forebrain.state.v9.<更旧>.sqlite`，内容随意）→ 跑 `Open` 迁移 → 断言目录恰好 1 个文件、名字＝本次新建的（含本次时间戳）、两个预放文件已删。
   - 新子用例「不匹配的文件不动」：同上场景预放 `notes.txt` 与 `my-restore.sqlite` → 迁移后两者仍在、旧命名备份已删。
   - 既有主用例（`len(entries) != 1` 断言）天然继续成立——确认不红。
   验证：`FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/state -run TestStreamDelta -count=1`（用例名以实际 Test 函数名为准，先 grep 确认；不在则跑 `./pkg/state -count=1` 全包）。
3. **全量**：`FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./... -count=1`；`go vet ./...`。

## 4. 边界

- **不动**：备份触发条件、空间检查、拒绝迁移路径（F1 四道闸）；备份文件命名；迁移框架本身；`pkg/gateway`、TUI 等一切其他包。
- **不做**：启动时清理、保留数量配置化、压缩旧份、跨目录备份。
- **明确不改**：F4 既有断言一个不删（`len(entries) != 1` 等继续有效）。

## 5. 完成判据（机器可查）

1. `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/state -count=1` 全绿（含两个新子用例）；`go vet ./...` 无输出。
2. 全量 `./...` 绿。
3. scratch home 手测脚本：预放 2 个旧命名备份 + 1 个无关文件 → 用 v11 fixture 驱动迁移 → `ls state/backups` 恰好 1 个本次备份 + 1 个无关文件；slog 行含 `removed 2`。

## 6. 维护注意

- 清理只认 `forebrain.state.v*.sqlite` 命名——未来若改备份命名（F5），同步改匹配规则。
- 计划 187 §8 的维护注意仍然成立且窗口收窄：backups 目录从此最多 1 份含 live 凭据的全库拷贝。

## 7. 逃生口

- 发现 `streamDeltaFixtureV11` 无法构造「目录里已有旧备份」的场景（如 fixture 每次全新建 home）→ 改为直接单测 `pruneOldStateBackups`（临时目录 + 预放文件 + 调函数断言），不改 fixture；仍不行则停，报告。
- 发现 backups 目录还有其他写入方（与 F3 矛盾）→ 停，列出来源再定匹配规则。
