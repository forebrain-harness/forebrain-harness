# Plan 019：状态库迁移——先备份，再清理已存的增量（回答/思考/工具输出）：已有完整记录的删除，没有的合并成段

> **执行者须知**：按步骤顺序执行，每步跑验证命令并确认预期结果后再进下一步。触发
> "STOP conditions" 时停下报告，不要即兴发挥。**不要更新 `plans/README.md`**（评审方维护索引）。
> **禁止提交代码**（owner 手动提交）：不建分支、不 commit、不 push、不 stash；改动留在工作区。
> **绝对不要对 `~/.forebrain/state` 下 owner 的真实状态库运行任何写操作或迁移**；只在测试与隔离 home 里验证。真实库只允许 `sqlite3 -readonly` 只读查询。
>
> **Drift check（先跑）**：plan 016、017、018 已执行（`grep -n 'func IsPushOnlyStreamEvent' pkg/event/run_events.go` 有 1 行）；
> `grep -n 'const stateSchemaVersion' pkg/state/schema_migrations.go` 记下当前版本 N（plan 015 执行后为 9）。本计划的迁移为 N+1。

## Status

- **Priority**: P2
- **Effort**: M
- **Risk**: HIGH（删除用户库里的数据；迁移在进程启动时运行，大库耗时长）
- **Depends on**: plan 016、017、018
- **Category**: migration
- **Planned at**: commit `005c1a8` + 主树工作区未提交改动，2026-10-10

## Why this matters

owner 裁决：库里已有的逐 token 增量（约 450 万行，几乎全部是 subagent 产生的 `reasoning_delta`/`assistant_delta`）**随迁移清理**——transcript 已覆盖的删除，
**迁移前自动备份状态库**。owner 2026-10-10 追加：**工具输出增量（`tool_output_delta`）一并清理**；~~迁移后不自动 VACUUM 回收空间~~（owner 2026-10-11 撤回此要求：照迁移框架现有行为，迁移结束若有空页就 VACUUM，`pkg/state/schema_migrations.go` 的 `migrateStateSchema` 末尾）。规则：改状态库必须迁移用户旧库。

关键约束（本机只读统计）：238 次 subagent 执行引用 222 个 worker 会话，但只剩 70 个 subagent 会话——**约 150 次执行的文字只存在于增量里**，直接删会永久丢失。
所以：**有 worker transcript 覆盖的执行 → 删增量；没有覆盖的 → 把连续增量合并成一段一条（保留在事件里，plan 017 对这类执行退回事件重放，显示不变）**。
主 agent 的增量（不带 `agent_id`）在 web 重载与 TUI 回放中本来就不用（transcript 承载），直接删除。
工具输出增量：工具完成后完整输出在完成步骤里（web 与 TUI 回放都用完成步骤整体替换卡片），所以**有同一步骤完成事件的 → 删除；没有的 → 把该步骤的分块合并成一条**（本机只读统计：19,394 行、15,425 个步骤，只有 6 个步骤没有完成事件）。

## Current state

- 工具输出增量的形状：`event_type = 'tool_output_delta'`，`payload_json` 为 `event.ToolCallOutputDeltaPayload`（`step_id`、`tool_name`、`text`、可选 `agent_id`…）；同一步骤的完成事件是同 `run_id` 下 `event_type = 'tool_call_completed'` 且 `json_extract(payload_json,'$.step_id')` 相同的那条。
- 增量的形状（真实数据）：`event_type` ∈ `assistant_delta`/`reasoning_delta`/`reasoning_done`，`payload_json` 如
  `{"agent_id":"subagent-287138d8-…","text":" me"}`；subagent 增量的 `run_id` = 该执行的 `execution_id`（`fb_runs` 里一条 `parent_run_id` 非空的子 run，
  其 `session_id` 是**父**会话）；`subagent_spawned` 的 payload 带 `execution_id`、`worker_session_id`（形如 `main:cli-…:…:…`）、`agent_id`。
- `fb_session_events` 表：`sequence INTEGER PRIMARY KEY AUTOINCREMENT, session_id, run_id, event_id, event_type, payload_json, occurred_at_ms`，`UNIQUE(session_id, event_id)`；
  索引 `idx_fb_session_events_session_type(session_id, event_type)`、`idx_fb_session_events_run_type(run_id, event_type)`。
- 迁移框架：`pkg/state/schema_migrations.go`——`stateSchemaVersion`、登记表 `:39-46`、数据迁移样板 `migrateStateV6ToV7`（`:152` 起：查询 → 内存处理 → 逐行 UPDATE）。
  `migrateStateSchema`（`:300` 起）负责加锁、读 `user_version`、逐个应用。
- 本机真实库约 3.7 GB；备份需要同量级的磁盘空间。

## Commands you will need

| 用途 | 命令 | 成功预期 |
|---|---|---|
| 测试 | `CGO_ENABLED=1 go test -tags fts5 ./pkg/state/... -count=1` | `ok` |
| 全量 | `CGO_ENABLED=1 go test -tags fts5 ./pkg/... -count=1` | `ok`（网络偶发用例单跑通过即可） |
| vet / fmt | `CGO_ENABLED=1 go vet -tags fts5 ./pkg/state/...`；`gofmt -l pkg/state` | 无输出 |

## Scope

**In scope**：`pkg/state/schema_migrations.go`（新迁移 + 备份）、`pkg/state/schema_migrations_test.go`（或新测试文件）、`pkg/state/schema.sql`（只改版本相关注释，若有）。

**Out of scope**：transcript 行本身；任何非增量事件；手动压缩入口。迁移框架末尾已有的自动 VACUUM 保持不动（owner 2026-10-11 撤回"不自动回收"）。

## Git workflow

不建分支、不 commit、不 push、不 stash。

## Steps

### Step 1：迁移前备份

在 `migrateStateSchema` 里，**仅当**即将应用 N+1 这一步（`user_version < N+1`）且库里存在至少一条 `IsPushOnlyStreamEvent` 类型的事件时，先备份：
`VACUUM INTO '<state 目录>/backups/forebrain.state.v<旧版本>.<UTC 时间戳>.sqlite'`（目录不存在则创建，权限 0700）。
- 写之前检查可用磁盘空间 ≥ 当前库文件大小 × 1.1（`syscall.Statfs` 或项目已有的磁盘工具；没有就加一个最小实现）；不足 → 迁移返回明确错误
  `state backup before migration v%d needs %s free; free some space and start again`，不修改库。
- 备份失败 → 迁移失败、不修改库。
- 日志记录备份路径（`slog.Info`），并在迁移成功后保留备份（不自动删除）。
（若 `VACUUM INTO` 不能在迁移所持的连接/事务里执行，把备份放到获取迁移锁之后、开始事务之前；做不到 → STOP。）

### Step 2：迁移 N+1 的数据处理

`migrateStateV<N>ToV<N+1>`：
1. **主 agent 增量**：`DELETE FROM fb_session_events WHERE event_type IN ('assistant_delta','reasoning_delta','reasoning_done') AND COALESCE(json_extract(payload_json,'$.agent_id'),'')=''`，
   分批执行（每批按 `sequence` 范围 5 万行，循环到删完），避免一次巨型事务。
2. **subagent 增量**，按执行（`run_id`）处理：
   - 找出所有有 subagent 增量的 `run_id`（`SELECT DISTINCT run_id FROM fb_session_events WHERE event_type IN (...) AND run_id IS NOT NULL`——走 `idx_fb_session_events_run_type`）。
   - 对每个 run_id：从同会话的 `subagent_spawned`（`json_extract(payload_json,'$.execution_id') = run_id`）读 `worker_session_id`。
   - **覆盖判定**：worker 会话存在于 `fb_sessions`，且其 `assistant`+`reasoning` 行的文字总长 ≥ 该执行 `assistant_delta`+`reasoning_delta` 文字总长的 90%。
   - 覆盖 → 删除该 run_id 的全部三类增量。
   - 未覆盖 → **合并**：按 `sequence` 顺序遍历该 run_id 的增量，把"同类型、同 `agent_id`、中间没有夹着该执行的其他事件"的连续一串合并成一条：保留这串第一条的
     `sequence`/`event_id`/`occurred_at_ms`，`payload_json` 的 `text` 改为整串文字拼接（保持 JSON 其他字段），删除其余行。`reasoning_done` 不合并（它是块边界，保留）。
     "中间夹着该执行的其他事件"用同一 `run_id` 下 sequence 介于两者之间的、三类文字增量以外的事件判断（`tool_output_delta` 也算"其他事件"，会打断合并）。
     本小节先于 3. 执行。
3. **工具输出增量**，按步骤 `(run_id, json_extract(payload_json,'$.step_id'))` 处理（主 agent 与 subagent 同一规则）：
   - 同 `run_id` 下存在同 `step_id` 的 `tool_call_completed` → 删除该步骤全部 `tool_output_delta`（分批，同 1.）。
   - 不存在 → 合并：按 `sequence` 把该步骤的全部分块合成一条（保留第一条的 `sequence`/`event_id`/`occurred_at_ms`，`text` 为整串拼接，其他 JSON 字段保持），删除其余行。
   - `step_id` 为空的行（不应存在）：统计数量，不删，写进日志与报告。
4. 每处理完一个 run_id 提交一次（或按框架的事务粒度；若框架要求整个迁移一个事务，分批 DELETE 在同一事务里也可，但须评估耗时并在报告写明）。
5. 迁移结束后的回收交给 `migrateStateSchema` 末尾已有的逻辑（有空页就 VACUUM，owner 2026-10-11 撤回"不自动回收"），本计划不改它；`slog.Info` 记录各类删除/合并的行数。不加手动压缩入口。
   注意：VACUUM 期间其他进程等写锁，大库（本机约 800MB 级）可能要几十秒到几分钟；在报告里写明本机实测耗时。

### Step 3：测试

`pkg/state` 加 `TestMigrateAwayStoredStreamDeltas`（照既有迁移测试造一个版本 N 的库，`grep -n 'func TestMigrateStateV' pkg/state/*_test.go` 找样板）：
- 会话 P 下：主 agent 增量 5 条 → 迁移后 0 条。
- 执行 E1：有 worker 会话 W1，W1 的 assistant/reasoning 行文字覆盖增量 → 迁移后 E1 的增量 0 条。
- 执行 E2：worker 会话不存在；增量序列为 `reasoning_delta×3, reasoning_done, assistant_delta×2, tool_call_started(E2), assistant_delta×2` →
  迁移后为 `reasoning_delta("拼接3段"), reasoning_done, assistant_delta("拼接2段"), tool_call_started, assistant_delta("拼接2段")`，文字拼接无损、顺序不变。
- 工具输出：步骤 S1 有 3 条 `tool_output_delta` 和一条同 `step_id` 的 `tool_call_completed` → 迁移后 S1 的增量 0 条、完成事件还在；步骤 S2 有 3 条增量、没有完成事件 → 迁移后 1 条，`text` 为 3 段拼接；主 agent 与 subagent 各造一组。
- 回收：迁移后库的 `PRAGMA freelist_count` = 0（框架末尾的 VACUUM 生效）。
- 备份：迁移后 `<state>/backups/` 下有一个可打开的 sqlite 文件，其中增量行数等于迁移前。
- 空间不足的模拟（把空间检查函数做成可替换的变量，测试里返回不足）→ 迁移返回错误、库未修改、`user_version` 未变。
- 已是 N+1 的库再启动不重复执行、不再备份。

**Verify**：`CGO_ENABLED=1 go test -tags fts5 ./pkg/state/ -run TestMigrateAwayStoredStreamDeltas -count=1 -v` → PASS；`./pkg/state/...` 全部 `ok`。

### Step 4：隔离 home 真机

复制一份**测试用**库（不是 owner 的真实库）：用 driver 在隔离 home 里跑出带 subagent 的会话（或用 Step 3 的造库函数生成一个文件），以旧版本二进制格式（`user_version` 调回 N）启动新二进制，
确认：启动日志打印备份路径；事件表增量数符合预期；web 重载（同一 home 起 gateway + 探针）与 TUI resume 的 subagent 视图文字与迁移前一致。记录启动耗时。

## Done criteria

- [ ] Commands 表全部通过
- [ ] `TestMigrateAwayStoredStreamDeltas` 覆盖上述 8 种情况且 PASS
- [ ] Step 4 结果（含耗时、备份路径、迁移前后增量数）写进报告
- [ ] 未对 owner 的真实状态库执行任何写操作
- [ ] `git status --short` 中本计划新增的改动只涉及 Scope 列出的文件

## STOP conditions

1. Drift check 不符（016/017/018 未完成，或版本号无法确定）。
2. `VACUUM INTO` 无法在迁移流程中执行。
3. 发现有 subagent 增量的 `run_id` 找不到对应的 `subagent_spawned`（无法判定覆盖）——统计数量后停下报告，不要猜。
4. 测试库上的迁移耗时按比例推算到 3.7 GB 库超过 5 分钟——停下报告推算值，由 owner 决定是否改为后台/分阶段迁移。

## Maintenance notes

- 备份文件放在 `<state>/backups/`，不自动清理；以后若加清理策略，要 owner 同意。
- 合并后的 subagent 增量（未覆盖的执行）是"历史只读"数据：plan 017 对这些执行走事件重放，显示与合并前一致。
- 本迁移之后，新数据不再产生增量行（plan 018）。
