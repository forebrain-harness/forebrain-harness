# SQLite 状态库整治计划：表结构、SQL、性能、并发与旧库迁移

> 目标：把 Forebrain Harness 的两个 SQLite 库（状态库 `forebrain.state.sqlite`、输出过滤历史库
> `outfilter/history.db`）整理成整洁、规范、可证明正确的最终形态——删掉无用表和无用字段、合并
> 冗余表与冗余字段、修掉低效与冗余的 SQL、修掉高并发下的竞态，并把用户已有的旧库**完整迁移**
> 到新形态（旧表、旧字段、旧索引、旧触发器清理干净）。
>
> 仓库硬规则（本计划的每一步都必须遵守）：
> 1. **输入 token 缓存命中率只升不降**。本计划不改 prompt 组装；但迁移必须保证每个会话重建出的
>    模型上下文（transcript 行的 `content`/`parts`、`fb_session_prompt_state.value`）逐字节不变，
>    见 §4.5 与 T11。
> 2. **只修根因，禁止防御式补丁**（nil 守卫、兜底分支、重试循环都不是修复）。
> 3. **死代码必须彻底删除**，用 Go 官方 `deadcode` 工具找，不用 grep 猜。
> 4. **顺带发现的缺陷纳入本计划一并修复**（§2.8 全部列成任务）。
> 5. **禁止任何 git commit / amend / merge / rebase**，所有改动留在工作区，由 owner 手工审阅提交。
> 6. **TUI 语义是黄金标准**；修复必须在真实 TUI + 真实模型下验证，web 端同样要实机验证。
> 7. 面向用户的错误提示只写一句话。

---

## 0. 元数据

| 项 | 值 |
| --- | --- |
| 优先级 | P1 |
| 工作量 | L（12 个任务 T0–T11，按编号顺序执行；后一个任务依赖前一个任务建立的迁移框架与 fixture） |
| 风险 | HIGH：改持久化层、事件回放链路，并迁移用户真实数据 |
| 类别 | tech-debt / perf / bug / migration / concurrency |
| 编写日期 | 2026-09-27 |
| 基线 | 仓库尚无任何提交（`git rev-parse HEAD` 报错），无法用 SHA 做漂移检查；改用 §1.3 的摘录校验 |

## 1. 执行者须知

### 1.1 Owner 裁决（2026-09-27，不得更改、不得重新论证）

1. **日志表一步到位合并**：删除 `fb_tool_audit`；把 `fb_run_steps` 合并进 `fb_session_events`
   （删除 `fb_run_steps` 表）。
2. **开启外键**：连接串加 `_foreign_keys=1`，为所有成立的引用关系声明 `FOREIGN KEY`，
   并修掉产生孤儿行的写入方（根因），不留孤儿。
3. **必须迁移旧库**：旧库数据保留并搬到新形态；**不再使用的旧表、旧字段必须清理干净**——
   迁移后的旧库与空库新建出来的库，`sqlite_master` 必须逐条一致。「清库重来」「版本不符拒绝
   打开」「自动删库」都不是可选项。

### 1.2 工作方式

- 每个任务（T0–T11）都有「步骤 → Verify 命令 → 期望结果」。Verify 不通过就修到通过；同一个
  Verify 修两次仍不过，按 §9 STOP。
- 所有实机验证（TUI、gateway、真实库迁移演练）**只在临时 FOREBRAIN home 上做**（把真实库复制
  过去），**T11 之前不得让开发中的二进制打开 `~/.forebrain` 下的真实库**：中间版本的迁移会把真实库
  标成 `user_version=1` 却不是最终形态。
- 找死代码：`$(go env GOPATH)/bin/deadcode ./...`（本机已安装；没有就
  `go install golang.org/x/tools/cmd/deadcode@latest`）。只删「本计划改动导致失去生产调用方」的
  条目；与本计划无关的既有死代码列进交付报告，不顺手删。
- 构建标签文件删除前要分别用 `GOOS=linux`、`GOOS=windows` 跑 `go vet`。

### 1.3 漂移校验（开工前先跑）

逐条执行，输出必须包含右列文本；任何一条对不上就 STOP（代码已漂移，本计划的摘录失效）。

| 命令 | 输出必须包含 |
| --- | --- |
| `sed -n 70,77p pkg/state/db.go` | `if err := addColumnsMissingFrom(ctx, db, declared); err != nil {` |
| `sed -n 319,345p pkg/state/db.go` | `IFNULL(m.source, 'transcript') = 'tui'` |
| `sed -n 458,466p pkg/state/runrt.go` | `SELECT ?, IFNULL(MAX(seq),0)+1, ?, ?, ? FROM fb_run_steps WHERE run_id=?` |
| `sed -n 40,46p pkg/state/message_sync.go` | `rows, err := s.listTranscriptStoredMessages(ctx, sessionID, 5000)` |
| `sed -n 350,353p pkg/tui/chat_turn.go` | `_ = s.runSvc().AppendToolAudit(stepCtx, rid, sid, evt.ToolName, detail)` |
| `sed -n 262,264p pkg/run/telemetry.go` | `AppendToolAuditFunctionCall(bg, rid, sid, tn, src, args, res, errTxt` |
| `sed -n 230,236p pkg/turn/scheduler.go` | `if err := s.Store.SaveJob(ctx, job); err != nil {` |
| `sed -n 306,309p pkg/state/cron.go` | `WHERE paused = 0 AND next_run_at > 0 AND next_run_at <= ? ORDER BY next_run_at ASC` |
| `sed -n 405,415p pkg/state/db.go` | `RawQuery: "mode=rwc&_busy_timeout=5000&_journal_mode=WAL",` |
| `sed -n 476,486p pkg/tool/loaded_skills.go` | `ensureColumn(db, "commands", "kind"` |

---

## 2. 审计结论

### 2.1 全景

项目有两个 SQLite 库：

| 库 | 定义位置 | 打开方式 |
| --- | --- | --- |
| 状态库 `<home>/state/forebrain.state.sqlite` | `pkg/state/schema.sql`（`//go:embed`），`pkg/state/db.go` | 注册驱动 `forebrain_sqlite`，DSN `mode=rwc&_busy_timeout=5000&_journal_mode=WAL` |
| 输出过滤历史 `<stateRoot>/state/outfilter/history.db` | `pkg/tool/loaded_skills.go:431-510` 内联 DDL | `sql.Open("sqlite3", path+"?_busy_timeout=5000")`，**无 WAL** |

内置 SQLite 版本 3.53.0（`github.com/mattn/go-sqlite3 v1.14.44`），支持 `STRICT`、`RETURNING`、
`DROP COLUMN`、`->`/`->>` JSON 运算符、`_txlock`/`_foreign_keys` DSN 参数。

状态库现有 21 张普通表 + 1 张 FTS5 虚表 + 2 个触发器。owner 本机库 795MB，行数：
`fb_messages` 70009、`fb_session_events` 62148、`fb_run_steps` 35295、`fb_tool_audit` 34029、
`fb_actions` 811、`fb_runs` 482、`fb_sessions` 282；另残留一张 schema.sql 里早已没有的 `fb_jobs`。

### 2.2 表级结论

| 表 | 结论 | 证据 |
| --- | --- | --- |
| `fb_work_items` | **删除** | 生产代码零引用（`grep -rlw fb_work_items pkg cmd --include=*.go` 只命中 schema）；前端 `frontend/src/lib/api.ts:670` 的 `TaskItemRecord` 是其残留类型，同样零使用 |
| `fb_todos` | **删除** | 生产代码零引用；todo 实际存于 `<home>/state/todos/<sid>.json`（`pkg/state/todo.go:32-38`）；它是 schema 里唯一声明了 FK 的表，但 FK 从未开启 |
| `fb_intel_audit` | **删除** | 生产代码零引用 |
| `fb_tool_audit` | **删除**（owner 裁决 1） | 每次工具调用被写两遍：`pkg/tui/chat_turn.go:352`/`pkg/gateway/server.go:1747` 写一条 `source=''`，`pkg/run/telemetry.go:263` 再写一条 `source=native/mcp/skill`；本机 34029 行里 16854+276+148 行来自后者、15945 行来自前者。内容全部可由工具完成事件推出 |
| `fb_run_steps` | **并入 `fb_session_events` 后删除**（owner 裁决 1） | 代码自称「compatibility run-step ledger」（`pkg/tui/notify.go:934`、`pkg/gateway/approval.go:74`）；gateway 已把每个 websocket 操作（含 `step`）镜像成规范事件写入 `fb_session_events`（`pkg/gateway/wsevents.go:66-128`）；goal 事件明确「写两遍」（`pkg/run/goal.go:293-305`）；子代理生命周期写三遍（子 run 一条 step、父 run 一条 step、一条规范事件，`pkg/run/subagent.go:1161-1164`、`1250-1260`） |
| `fb_jobs`（仅存在于旧库） | **迁移时删除** | schema.sql 与代码均无；`CREATE TABLE IF NOT EXISTS` 机制删不掉它 |
| 其余 16 张 + FTS | 保留，逐表整治（§2.3） | — |

### 2.3 字段级结论

「只写不读」= 有写入语句、生产代码无任何读取（SELECT 列表、WHERE、Go 字段消费者都没有）。

| 表.字段 | 问题 | 处置 |
| --- | --- | --- |
| `fb_sessions.prompt_tokens` `completion_tokens` `cost` | 只被 `pkg/migrate/sessions.go:330` 写入，从未读取；真实用量在 `fb_runs.usage_*` | 删 |
| `fb_sessions.message_count` | 只被开库归一化（按已不存在的 `source='tui'` 计数，恒为 0）和 migrate 写入，从未读取 | 删 |
| `fb_sessions.todos` | 零引用 | 删 |
| `fb_sessions.compact_boundary_id` | TEXT 存整数行号，读时 `strconv` 解析 | 改为 `compact_boundary_message_id INTEGER REFERENCES fb_messages(id)` |
| `fb_sessions.context_reset_row_id` | 指向 fb_messages 行号，0 表示无 | 改为 `context_reset_message_id INTEGER REFERENCES fb_messages(id)`，NULL 表示无 |
| `fb_sessions.parent_session_id` `project_id` | `''` 充当「无」，无法加 FK | NULL 表示无 + FK |
| `fb_sessions.origin` | `''` 表示原生 | `'native'`/`'migrated'` + CHECK |
| `fb_sessions.title` | 可空，读处到处 `IFNULL(title,'')` | `NOT NULL CHECK (TRIM(title)<>'')` |
| `fb_messages.source` | 名字误导：实际是可见性（`transcript`/`transcript_repaired`/`transcript_withdrawn`，`pkg/state/session_store.go:1328-1331`），与 `fb_files.source`、`fb_sessions.memory_source` 同名异义；所有读取都写成 `IFNULL(source,'transcript')='transcript'`，使索引失效 | 改名 `visibility`，取值 `visible`/`repaired`/`withdrawn`，NOT NULL + CHECK |
| `fb_messages.updated_at` | 只写不读（只有已删的归一化与触发器读） | 删 |
| `fb_messages.finished_at` | 读入 `Message.FinishedAt` 后无消费者 | 删（T2 用编译器验证） |
| `fb_messages.run_started_at` `run_finished_at` `worked_duration_ms` | ① run 级事实逐行重复：同一 run 的所有 assistant 行值完全相同（本机 292/292 个 run）；② 一列两义：tool 行上存单个工具执行计时、`!cmd` 用户行上存无 run 的命令计时；③ 前两列是 TEXT(RFC3339Nano)，全库唯一的 TEXT 时间戳；④ 12935 条带计时的 assistant 行中 7873 条没有 `run_id`，TUI 回放因此不给这些回合画「Worked for」，gateway 为此保留一套按时间窗口猜 run 的遗留代码 | run 计时迁到 `fb_runs.started_at_ms/finished_at_ms/worked_ms`；消息行只保留本行执行计时 `exec_started_at_ms/exec_finished_at_ms/exec_duration_ms`（仅 tool 行与 `!cmd` 行，CHECK 约束）；迁移回填旧行 `run_id`（T4） |
| `fb_messages.run_id` | `''` 表示无 | NULL + FK |
| `fb_runs.token_estimate` | `rune数/4` 的估值，只写进 JSON，前端不读 | 删 |
| `fb_runs.parent_run_id` | `''` 表示无，且**无索引**（递归 CTE 每层全表扫） | NULL + FK + 部分索引 |
| `fb_run_waits.source_json` | JSON 里藏着 9 个可查询字段（租约 owner、租约时间、阶段…），5 个函数用「读整段 JSON→改→`WHERE source_json=旧整段`」的 CAS，最多重试 8 次（`pkg/state/runrt.go:791-958`） | 拆成类型化列，CAS 改单条条件 UPDATE（列数增加是规范化，理由见 §3.3） |
| `fb_run_waits` 主键 `(run_id, action_id)` | `SetWaitingAction` 每次写入后删掉该 run 的其它行（`runrt.go:499`），一个 run 至多一条 wait | 主键改 `run_id`，`action_id UNIQUE` |
| `fb_actions` | **没有 session 列**：会话归属藏在 `payload_json.session_id`，取不到时再走 `fb_run_waits→fb_runs` 反查（`pkg/gateway/server.go:183-202`）；列表接口因此整表读出再在 Go 里逐行过滤 | 新增 `session_id NOT NULL REFERENCES fb_sessions` |
| `fb_session_events.schema_version` | 每行恒为 1（`SessionEventSchemaVersion`），页级已有同名字段 | 删；线上 `RunEvent.SchemaVersion` 读时填常量 |
| `fb_session_events.run_id` | `''` 表示无 | NULL + FK |
| `fb_files` 28 列 | `project_id`（只写不读，所有 SELECT 都不选它）、`run_id`（只进 JSON，前端 `FileInfo` 只用 id/originalName/mediaType，`frontend/src/lib/api.ts:741-745`）、`oss_etag` `oss_endpoint`（只写不读）、`oss_version_id`（恒为 ''）、`parsed_text_backend`（恒为 'local'）、`parsed_text_oss_bucket/key/etag`（从未写入非空值）、`path` `content` `version` `source`（为 `source='workspace_file'` 设计，**零写入方**，连同其部分唯一索引都是死结构）；`storage_relpath` 与 `oss_key` 互斥表达同一概念 | 删 12 列；`storage_relpath`+`oss_key` 合并为 `storage_key`，`oss_bucket` 改名 `storage_bucket`，`error` 改名 `parse_error`（它只由解析失败写入），`parsed_text_relpath` 改名 `parsed_text_path` |
| `fb_cron_runs.run_id` | 所有写入方都不设置（`pkg/turn/scheduler.go:157-163` 的 `CronRun{}` 无 RunID），恒为 '' | 删 |
| `fb_cron_runs.agent_id` | `DEFAULT ''` 却是租户键 | `NOT NULL CHECK (TRIM(agent_id)<>'')` |
| `fb_heartbeats.paused` | 与 `next_run_at` 重复：`ApplyHeartbeatInterval` 在 paused 时把 `next_run_at` 置 0（`scheduler.go:372-376`） | 删 `paused`，`next_run_at IS NULL` 即暂停 |
| `fb_heartbeats.agent_id` | 冗余（会话已有 agent_id），且到期查询根本不按它过滤（缺陷 D12） | 删，按 `fb_sessions.agent_id` JOIN 取租户 |
| 0 充当「无时间」 | `fb_projects.archived_at`、`fb_cron_jobs.last_run_at`、`fb_cron_runs.finished_at`、`fb_heartbeats.last_fired_at/next_run_at` | 统一 NULL 表示无 |
| `fb_memory_jobs.worker_id` | 只写不读（真正的 fencing 是 `ownership_token`） | 删 |
| `fb_memory_stage1_outputs.usage_count` | 可空，查询处处 `COALESCE(usage_count,0)` | `NOT NULL DEFAULT 0` |
| `fb_memory_stage1_outputs.agent_id` | 与 `fb_sessions.agent_id` 冗余，但所有记忆查询以它为租户键走索引 | 保留，加复合 FK `(thread_id, agent_id) → fb_sessions(id, agent_id)`，由数据库保证一致 |
| history.db `commands.timestamp` | TEXT（go-sqlite3 的 time.Time 字符串） | `created_at_ms INTEGER` |
| history.db 表名 `commands` | 同时存 shell 与 MCP 记录，名字误导且无前缀 | 改名 `output_records`，`cmd` 改名 `command` |

### 2.4 索引结论

| 索引 | 结论 | 证据 |
| --- | --- | --- |
| `idx_fb_sessions_updated` | 删 | 所有会话列表都带 `agent_id`，走 `idx_fb_sessions_agent_updated` |
| `idx_fb_messages_session` / `_session_source_created` / `_message_id` / `_session_run` | 4 个合并为 `idx_fb_messages_session_visibility(session_id, visibility)` | 没有任何查询按 `message_id` 或 `run_id` 过滤；rowid 隐式附在索引尾部，`WHERE session_id=? AND visibility='visible' ORDER BY id` 直接走索引序 |
| `idx_fb_runs_status(status, updated_at)` | 改为部分索引 `WHERE status IN ('running','waiting_action')` | 只有「找活跃 run」用到 status |
| 缺 `fb_runs(parent_run_id)` | **新增** | EXPLAIN：递归 CTE 每次执行都现建 `AUTOMATIC COVERING INDEX`，`ListChildRuns` 全表 `SCAN fb_runs` |
| `idx_fb_session_events_run_sequence` | 改为 `(run_id, event_type)` 部分索引 | 合并后 run 账本按 run_id(+type) 读 |
| `fb_files` 9 个二级索引 | 只留 `(session_id)`（FK 子键） | 所有查询只按主键 `id` 访问 |
| `idx_fb_actions_kind` | 删 | 无按 kind 的查询 |
| `idx_fb_actions_status(status, updated_at)` | 改为 `(session_id, status, updated_at)` + 部分索引 `(created_at) WHERE status='pending'` | 过期扫描按 created_at，列表按会话 |
| `idx_fb_todos_*` `idx_fb_intel_audit_*` `idx_fb_tool_audit_*` `idx_fb_run_steps_run` `idx_fb_work_items_*` | 随表删除 | — |
| history.db `idx_outfilter_commands_ts` `_kind_ts` | 删 | 只有按 id 取与全表聚合两类查询 |

### 2.5 SQL 语句：低效、冗余、可删除

在 scratch 目录用当前 `schema.sql` 造了一个大库（2000 会话 × 500 消息 = 100 万条消息、6 万 run、
20 万 action、FTS 50 万行）实测，每条取 2–5 次平均（脚本见附录 A）：

| # | 语句 / 位置 | 实测 | 问题 | 处置 |
| --- | --- | --- | --- | --- |
| S1 | `SessionIDWithLatestMessage`（`pkg/state/session_store.go:512`），TUI 每次启动调用（`pkg/tui/chat_surface.go:199`） | **3331 ms** | 扫该租户全部消息 + 两次临时 B-tree 排序，O(消息总数) | 改按 `fb_sessions(agent_id, updated_at)` 索引取第一条有可见消息的会话：**0.0 ms** |
| S2 | `normalizeSessionsTx` 4 条全表 UPDATE（`pkg/state/db.go:319-347`），**每次开库**在写事务里执行 | 其中 2 条 **1941 ms** | 防御式修补反范式列，O(消息总数) 且持写锁；写入路径本已维护这些列 | 整段删除（S2 连同 `message_count` 列、`'tui'` 触发器一起删） |
| S3 | `SessionUsageForSession` 递归 CTE（`runrt.go:363`） | **300 ms** | `parent_run_id` 无索引 | 加索引后 **1.1 ms** |
| S4 | `ListChildRuns`（`runrt.go:1099`） | 26.6 ms | 全表扫 | 加索引后 0.0 ms |
| S5 | `ListRunsRecent`（`runrt.go:1021`）+ `findCancellablePrimaryRun`（`pkg/gateway/agents_api.go:369-389`） | 131.8 ms | 全局按 updated_at 全表排序，取 200 行后在 Go 里筛「顶层且运行中」——跨租户、而且第 201 条以后的活跃 run 会被漏掉（缺陷 D13） | 删 `ListRunsRecent`，改单条 SQL：租户 JOIN + `parent_run_id IS NULL` + 部分索引 |
| S6 | `handleActions`（`pkg/gateway/api_extra.go:1766-1826`）调 `Actions.List(status, -1)` | 全量 **1693 ms**（再加每行 1–3 次 N+1 查询） | 按会话/agent 过滤时读整表，逐行 `sessionOwned` + `FindRunByAction` + `GetRun` | `fb_actions.session_id` 列 + 一条 JOIN 查询 |
| S7 | FTS 删除 `DELETE FROM fb_memory_fts WHERE root=? AND path=?`（`pkg/memory/ftsindex.go:137`） | 单文件 **1313 ms** | UNINDEXED 列过滤 = 扫整张 FTS；刷新 N 个文件就是 N 次全扫 | 记录每个文件的 rowid 区间，按 `rowid >= ? AND rowid < ?` 删除 |
| S8 | `ExpirePending`（`pkg/state/action_service.go:284-327`） | — | 先 SELECT 全部 id，再逐条 UPDATE（N+1） | `UPDATE … WHERE status='pending' AND created_at<? RETURNING id` 一条 |
| S9 | 5 个 wait 租约函数（`runrt.go:744-958`） | — | 读 JSON→改→整段比较 CAS，失败重试 8 次 | 类型化列上的单条条件 UPDATE，无读、无循环 |
| S10 | `AppendMessageWithSourceForRun`（`session_store.go:1085-1141`） | — | 每条消息：`Ensure` 前 1 次、后 1–2 次（`SetTitle` 内部再 `Ensure` 一次）+ `COUNT(*)` 判断首条用户消息，**4–6 条自动提交语句** | 一个事务：1 次 upsert + 1 次 INSERT + （用户消息时）1 次条件改标题 |
| S11 | `AppendStructuredMessageForRun` + `touchSessionFromTurn`（`session_store.go:1147-1204`、`1509-1539`） | — | 同 S10 | 同 S10 |
| S12 | 3 条几乎相同的 `INSERT INTO fb_messages`（`session_store.go:1110`、`1182`、`1657`） | — | 重复 | 合并为一个 `insertMessage` |
| S13 | 4 份相同的消息列清单 + Scan（`session_store.go:632`、`826`、`937`、`960`） | — | 重复 | 一个 `messageColumns` 常量 + 一个 `scanMessage`（仿 `project_store.go:76-90` 的 `projectColumns`） |
| S14 | `LastTranscriptRowID`（`:805`）与 `LatestTranscriptRowID`（`:1032`） | — | 同一条 SQL 两个函数 | 合并为一个（保留带 `requireOwned` 的语义） |
| S15 | `LatestContentBySource`（`:1595`） | — | 零生产调用方 | 删 |
| S16 | `AppendStep` 的 `UPDATE fb_runs SET updated_at`（`runrt.go:465`）及整个 `AppendStep` | — | 表合并后整体消失 | 删 |
| S17 | `AppendToolAudit*` 2 条 INSERT、`ListToolAudit*` 2 条 SELECT | — | 表删除 | 删 |
| S18 | `AppendSessionEventOnce`（`pkg/state/session_events.go:80-99`） | — | 每次插入后无条件再 SELECT 一次 | `INSERT … ON CONFLICT DO NOTHING RETURNING sequence`；只有冲突时才 SELECT |
| S19 | 项目删除时 `UPDATE fb_sessions SET project_id='' WHERE project_id=?`（`pkg/state/project_store.go:284`） | — | FK `ON DELETE SET NULL` 后由库完成 | 删 |
| S20 | `applyStateSchema` 每次开库：内存里建一遍 schema、逐表 `PRAGMA table_info`（`db.go:65-212`） | — | 加法式补列迁移，删不掉任何东西 | 删，换成 `PRAGMA user_version` 一次读取 |
| S21 | history.db `ensureColumn` 3 次（`loaded_skills.go:476-486`） | — | 同 S20 | 删，换成版本化迁移 |
| S22 | `GetWaitForRun` 的 `ORDER BY CASE WHEN tool_name='long_run_async_switch' …`（`runrt.go:979`） | — | 一 run 一 wait 后排序无意义 | 按主键取 |
| S23 | `DueJobs` 的 `enabled = 1`（`pkg/state/cron.go:183`） | — | `ApplySchedule` 对停用任务已把 `next_run_at` 置空（`scheduler.go:346-349`），谓词冗余 | 删谓词，索引改 `(agent_id, next_run_at) WHERE next_run_at IS NOT NULL` |
| S24 | 所有对 NOT NULL 列的 `IFNULL(...)`/`COALESCE(...)` | — | 噪音，且包在索引列上会让索引失效 | 表改 NOT NULL 后在同一任务内删掉 |

### 2.6 并发竞态

gateway 生产是多副本集群；本机常见 TUI 与 gateway 两个进程共享一个库；进程内又有多 goroutine
经 `database/sql` 连接池并发写。当前 DSN 没有 `_txlock`，**所有 `BeginTx` 都是 DEFERRED 事务**。

| # | 竞态 | 后果 | 根因修法 |
| --- | --- | --- | --- |
| R1 | DEFERRED 事务先读后写（如 `ForkInto`、记忆 `store.go:310/382/409`）在 WAL 下遇到别的写者先提交时，升级写锁直接返回 `SQLITE_BUSY_SNAPSHOT`，**busy_timeout 不生效** | 偶发写失败；很多调用点 `_ =` 吞错 → 静默丢数据 | DSN 加 `_txlock=immediate`：写事务一开始就拿写锁，由 busy_timeout 排队。记忆包手写的 `conn` + `BEGIN IMMEDIATE`（`pkg/memory/store.go:470-476`、`jobs.go:339-345`）随之改回普通 `BeginTx` |
| R2 | `AppendMessageSequenceForRun`（`pkg/state/message_sync.go:34-63`）先读 transcript、在 Go 里算出要追加的后缀、再逐行插入，全程无事务 | 两个写者（TUI 与 gateway 同会话、审批恢复与收尾写入）同时算出同一后缀 → **重复行**。`RepairDanglingToolResults` 注释里「duplicate or misattached tool-result rows」与 message_sync 注释里「same user message … replayed several times」都是这个症状 | 读、规划、写入放进同一个 IMMEDIATE 事务 |
| R3 | 首条用户消息命名：插入后 `COUNT(*)==1` 才生成标题（`session_store.go:1549-1566`） | 两条用户消息并发插入时两边都数到 2 → 会话永远不被命名 | 改为原子条件更新：`UPDATE fb_sessions SET title=? WHERE id=? AND agent_id=? AND title=id` |
| R4 | cron `recordOutcome`（`pkg/turn/scheduler.go:208-236`）把**开火前**读到的整行快照在任务跑完后 `SaveJob` 全行 upsert 回去 | 运行期间用户暂停/改提示词会被**还原**；运行期间删除的任务会被 `INSERT … ON CONFLICT` **复活** | 结果只更新结果列（`last_*`、`failure_streak`）；`WHERE id=?` 更新 0 行即表示已删，不复活 |
| R5 | cron/heartbeat 的「防重复开火」只有进程内 `s.claim`（`scheduler.go:114`、`272`），`next_run_at` 要等任务跑完才推进 | 两个进程（集群副本、或重启交叠）同时认领同一到期任务 → **重复执行**并重复投递 | 开火前用 CAS 推进：`UPDATE … SET next_run_at=<下一次>, run_count=run_count+1 WHERE id=? AND next_run_at=<读到的到期值>`，只有更新到 1 行的进程开火 |
| R6 | heartbeat `reanchor`（`scheduler.go:297-310`）用快照 `SaveHeartbeat` 全行 upsert | 触发期间被清除的心跳被复活、被暂停的心跳被恢复 | 条件 UPDATE，`WHERE session_id=? AND next_run_at IS NOT NULL` |
| R7 | `RepairDanglingToolResults`（`session_store.go:1413`）读 transcript 后逐行改写，无事务 | 与并发追加交错时可能剥掉刚写入的 tool_calls | 放进 IMMEDIATE 事务 |
| R8 | wait 租约 CAS（S9）靠整段 JSON 比较 + 8 次重试 | 租约心跳刷新 `resume_claimed_at` 会让并发的 `BeginWaitResumeExecution` 比较失败并耗尽重试 | 列级条件 UPDATE，互不干扰的字段不再互相冲突 |
| R9 | history.db 无 WAL、只靠进程内 `s.mu` 互斥 | TUI 与 gateway 同时写时读写互斥、超时报错 | 与状态库相同的 DSN（WAL + `_txlock=immediate` + busy_timeout） |

`AppendStep` 用单条 `INSERT … SELECT MAX(seq)+1` 分配序号本身是原子的（SQLite 单写者），随表删除。

### 2.7 迁移机制现状

`applyStateSchema`（`pkg/state/db.go:53-212`）只会给已有表**补**缺的列；表、列、索引、触发器一旦
废弃就永远留在用户库里（owner 本机的 `fb_jobs` 就是证据），也无法改类型、改约束、加 FK。
history.db 的 `ensureColumn` 同理。owner 已裁决：改为**版本化迁移**，迁移后结构必须与新建库一致。

### 2.8 顺带发现的缺陷（全部在本计划内修复）

| # | 缺陷 | 位置 | 修复任务 |
| --- | --- | --- | --- |
| D1 | 工具审计双写，且两路 `session_id` 语义不同（一路是会话 id，一路是子代理复合键 `main:cli-…:…`，本机 1667 行指向不存在的会话） | `chat_turn.go:352`、`server.go:1747`、`telemetry.go:246-264` | T5 |
| D2 | `/tool-audit` 先 `LIMIT` 后在 Go 里按 run/tool/source 过滤 → 过滤结果残缺 | `api_extra.go:1462-1507` | T5 |
| D3 | `/tool-audit`、`/cost-summary`、`/rewind-last` 不校验会话归属（跨租户可读/可改） | `api_extra.go:1395-1540` | T5 |
| D4 | `/rewind-last` 在 detail 顶层找 `abs_path/file_path/path`，而 detail 是 `{input,output,…}` → 永远 400 | `api_extra.go:1415-1433` | T5 |
| D5 | `/cost-summary` 因双写把工具调用数算成两倍 | `api_extra.go:1524-1535` | T5 |
| D6 | 并发追加 transcript 产生重复行（R2） | `message_sync.go` | T2 |
| D7 | 首条消息命名竞态（R3） | `session_store.go:1549` | T2 |
| D8 | DEFERRED 事务升级失败（R1） | `db.go:414` | T1 |
| D9 | cron 结果写回还原用户修改、复活已删任务（R4） | `scheduler.go:208-236` | T7 |
| D10 | cron/heartbeat 跨进程重复开火（R5） | `scheduler.go` | T7 |
| D11 | heartbeat 复活/解除暂停（R6） | `scheduler.go:297-310` | T7 |
| D12 | `DueHeartbeats` 不按租户过滤，调度器会给非当前 primary agent 的会话开火 | `cron.go:300-315` | T7 |
| D13 | `findCancellablePrimaryRun` 跨租户且 200 行后漏检（S5） | `agents_api.go:369-389` | T3 |
| D14 | actions 列表全表扫 + N+1（S6） | `api_extra.go:1766-1826` | T3 |
| D15 | `GET /files/:id`、下载接口不校验会话归属 | `api_extra.go:655-700` | T6 |
| D16 | 首条消息之前 MCP 启动失败会写一条 `turn_error` 事件到尚不存在的会话（本机 5 行孤儿） | `pkg/tui/notify.go:966-995` 及其发布方 | T5 |
| D17 | `CreateSubagentRun` 在会话 id 为空时写入虚构会话 `"default"` | `runrt.go:238-241` | T3 |
| D18 | `SetStatus` 在存储层写 `"error"` 步骤事件（越层，且与表面层的 `turn_error` 重复） | `runrt.go:996-1009` | T5 |
| D19 | schema 注释称 cron「NULL 即暂停、不再查第二个标志」，但 `DueJobs` 仍查 `enabled` | `schema.sql` cron 注释、`cron.go:183` | T7 |
| D20 | 7873 条没有 `run_id` 的旧回合在 TUI 回放里没有「Worked for」行（`replayWorkedLine` 要求 RunID 非空），gateway 另用一套按时间窗口猜 run 的遗留代码弥补 | `pkg/tui/commands.go:361-375`、`pkg/gateway/api_extra.go:1056-1260` | T4 |

---

## 3. 目标形态

### 3.1 约定（所有表一律遵守）

1. 所有普通表 `STRICT`；小型复合主键表加 `WITHOUT ROWID`。
2. 引用另一张表的列：可空 TEXT/INTEGER + `REFERENCES`，**NULL 表示「无」**，不再用 `''`/`0` 哨兵。
   子键列建索引（SQLite 在父行删除时按子键查找）。
3. 时间：秒级 `*_at INTEGER`；毫秒级一律 `_ms` 后缀；「没有这个时间」一律 NULL。不再有 TEXT 时间戳。
4. 枚举列一律 `CHECK (col IN (...))`，取值与 Go 常量一致。
5. 布尔：`INTEGER NOT NULL CHECK (col IN (0,1))`。
6. 非引用的文本列：`NOT NULL DEFAULT ''`；查询里不再出现对 NOT NULL 列的 `IFNULL`/`COALESCE`。
7. 只保留有查询使用的索引，每个索引在 §2.4 或任务里写明对应的查询。
8. schema.sql 只用于**空库**：语句不带 `IF NOT EXISTS`；已有库只能经 `schema_migrations.go` 到达这个形态。

### 3.2 最终 `pkg/state/schema.sql`

T1–T8 各自把自己负责的表改成下面的样子；T11 时全文必须与此一致（注释可以更详细，结构不能不同）。
`fb_sessions` 与 `fb_messages` 互相引用，SQLite 允许（FK 在 DML 时检查，不在建表时检查）。

现 schema.sql 首行的 `PRAGMA journal_mode = WAL;` **删除**：新框架在 `BEGIN IMMEDIATE` 事务内执行
schema.sql，而事务内的 `PRAGMA journal_mode` 静默不生效（已实测：`BEGIN IMMEDIATE; PRAGMA journal_mode=WAL; …; COMMIT;`
之后库仍是 `delete` 模式）。WAL 由 DSN 的 `_journal_mode=WAL` 在每个连接上设置，这是唯一来源。

本节 DDL 已在 SQLite 3.43 上整体执行通过（16 张普通表、24 个二级索引、复合 FK 生效、
`ON DELETE SET NULL` 行为符合预期）。

```sql
CREATE TABLE fb_projects (
  id TEXT PRIMARY KEY,
  agent_id TEXT NOT NULL CHECK (TRIM(agent_id) <> ''),
  name TEXT NOT NULL,
  icon TEXT NOT NULL DEFAULT '',
  description TEXT NOT NULL DEFAULT '',
  instructions TEXT NOT NULL DEFAULT '',
  root TEXT NOT NULL,
  project_key TEXT NOT NULL,
  memory_scope TEXT NOT NULL DEFAULT 'shared' CHECK (memory_scope IN ('shared', 'project_only')),
  resource_access INTEGER NOT NULL DEFAULT 1 CHECK (resource_access IN (0, 1)),
  pinned INTEGER NOT NULL DEFAULT 0 CHECK (pinned IN (0, 1)),
  archived_at INTEGER,
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL
) STRICT;
CREATE INDEX idx_fb_projects_agent_updated ON fb_projects(agent_id, updated_at DESC);
CREATE INDEX idx_fb_projects_agent_root ON fb_projects(agent_id, root);

CREATE TABLE fb_sessions (
  id TEXT PRIMARY KEY,
  agent_id TEXT NOT NULL CHECK (TRIM(agent_id) <> ''),
  title TEXT NOT NULL CHECK (TRIM(title) <> ''),
  parent_session_id TEXT REFERENCES fb_sessions(id) ON DELETE SET NULL,
  project_id TEXT REFERENCES fb_projects(id) ON DELETE SET NULL,
  origin TEXT NOT NULL DEFAULT 'native' CHECK (origin IN ('native', 'migrated')),
  cwd TEXT NOT NULL DEFAULT '',
  git_branch TEXT NOT NULL DEFAULT '',
  memory_mode TEXT NOT NULL DEFAULT 'disabled',
  memory_source TEXT NOT NULL DEFAULT '',
  initial_window_id TEXT NOT NULL DEFAULT '',
  compact_boundary_message_id INTEGER REFERENCES fb_messages(id) ON DELETE SET NULL,
  context_reset_message_id INTEGER REFERENCES fb_messages(id) ON DELETE SET NULL,
  created_at INTEGER NOT NULL CHECK (created_at > 0),
  updated_at INTEGER NOT NULL CHECK (updated_at > 0),
  UNIQUE (id, agent_id)
) STRICT;
CREATE INDEX idx_fb_sessions_agent_updated ON fb_sessions(agent_id, updated_at DESC);
CREATE INDEX idx_fb_sessions_agent_project ON fb_sessions(agent_id, project_id, updated_at DESC);
CREATE INDEX idx_fb_sessions_parent ON fb_sessions(parent_session_id) WHERE parent_session_id IS NOT NULL;

CREATE TABLE fb_messages (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  session_id TEXT NOT NULL REFERENCES fb_sessions(id) ON DELETE CASCADE,
  run_id TEXT REFERENCES fb_runs(id) ON DELETE SET NULL,
  role TEXT NOT NULL CHECK (role IN ('user', 'assistant', 'tool', 'reasoning', 'system')),
  visibility TEXT NOT NULL DEFAULT 'visible' CHECK (visibility IN ('visible', 'repaired', 'withdrawn')),
  content TEXT NOT NULL,
  parts TEXT NOT NULL DEFAULT '[]',
  message_id TEXT NOT NULL DEFAULT '',
  model TEXT NOT NULL DEFAULT '',
  usage_json TEXT NOT NULL DEFAULT '',
  tool_step_id TEXT NOT NULL DEFAULT '',
  tool_meta_json TEXT NOT NULL DEFAULT '',
  exec_started_at_ms INTEGER,
  exec_finished_at_ms INTEGER,
  exec_duration_ms INTEGER,
  created_at INTEGER NOT NULL,
  -- Execution timing belongs to the row's own work: a tool call, or a shell
  -- command the user ran. A run's timing lives on fb_runs.
  CHECK (exec_started_at_ms IS NULL OR role IN ('tool', 'user')),
  CHECK ((exec_started_at_ms IS NULL) = (exec_finished_at_ms IS NULL)
     AND (exec_finished_at_ms IS NULL) = (exec_duration_ms IS NULL))
) STRICT;
CREATE INDEX idx_fb_messages_session_visibility ON fb_messages(session_id, visibility);
CREATE INDEX idx_fb_messages_run ON fb_messages(run_id) WHERE run_id IS NOT NULL;

CREATE TABLE fb_session_ui_state (
  session_id TEXT NOT NULL REFERENCES fb_sessions(id) ON DELETE CASCADE,
  surface TEXT NOT NULL,
  state_json TEXT NOT NULL DEFAULT '{}',
  updated_at INTEGER NOT NULL,
  PRIMARY KEY (session_id, surface)
) STRICT, WITHOUT ROWID;

CREATE TABLE fb_session_prompt_state (
  session_id TEXT NOT NULL REFERENCES fb_sessions(id) ON DELETE CASCADE,
  key TEXT NOT NULL,
  value TEXT NOT NULL,
  updated_at INTEGER NOT NULL,
  PRIMARY KEY (session_id, key)
) STRICT, WITHOUT ROWID;

CREATE TABLE fb_runs (
  id TEXT PRIMARY KEY,
  session_id TEXT NOT NULL REFERENCES fb_sessions(id) ON DELETE CASCADE,
  parent_run_id TEXT REFERENCES fb_runs(id) ON DELETE CASCADE,
  input_text TEXT NOT NULL,
  status TEXT NOT NULL CHECK (status IN ('running', 'waiting_action', 'done', 'failed', 'cancelled')),
  usage_prompt_tokens INTEGER NOT NULL DEFAULT 0,
  usage_completion_tokens INTEGER NOT NULL DEFAULT 0,
  usage_cache_read_tokens INTEGER NOT NULL DEFAULT 0,
  usage_cache_creation_tokens INTEGER NOT NULL DEFAULT 0,
  usage_llm_calls INTEGER NOT NULL DEFAULT 0,
  started_at_ms INTEGER,
  finished_at_ms INTEGER,
  worked_ms INTEGER,
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL,
  -- The surface that ran the turn owns its clock and records all three when
  -- the run completes; a run that never completed has none.
  CHECK ((started_at_ms IS NULL) = (finished_at_ms IS NULL)
     AND (finished_at_ms IS NULL) = (worked_ms IS NULL))
) STRICT;
CREATE INDEX idx_fb_runs_session ON fb_runs(session_id, updated_at DESC);
CREATE INDEX idx_fb_runs_parent ON fb_runs(parent_run_id) WHERE parent_run_id IS NOT NULL;
CREATE INDEX idx_fb_runs_active ON fb_runs(status, updated_at DESC) WHERE status IN ('running', 'waiting_action');

-- The single, ordered record of everything a conversation and its runs did.
-- sequence is the conversation cursor surfaces page by; run_id scopes a row to
-- the run that produced it, which is how run-level readers (goal state, the
-- run events API, tool audit) read the same log.
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

CREATE TABLE fb_actions (
  id TEXT PRIMARY KEY,
  session_id TEXT NOT NULL REFERENCES fb_sessions(id) ON DELETE CASCADE,
  kind TEXT NOT NULL,
  status TEXT NOT NULL CHECK (status IN ('pending', 'answered', 'approved', 'denied', 'cancelled', 'expired', 'error')),
  payload_json TEXT NOT NULL,
  answer_json TEXT NOT NULL DEFAULT '',
  error TEXT NOT NULL DEFAULT '',
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL
) STRICT;
CREATE INDEX idx_fb_actions_session_status ON fb_actions(session_id, status, updated_at DESC);
CREATE INDEX idx_fb_actions_pending_created ON fb_actions(created_at) WHERE status = 'pending';

CREATE TABLE fb_run_waits (
  run_id TEXT PRIMARY KEY REFERENCES fb_runs(id) ON DELETE CASCADE,
  action_id TEXT NOT NULL UNIQUE REFERENCES fb_actions(id) ON DELETE CASCADE,
  tool_name TEXT NOT NULL,
  tool_input_json TEXT NOT NULL,
  session_snapshot_json TEXT NOT NULL DEFAULT '',
  agent_id TEXT NOT NULL DEFAULT '',
  subagent_type TEXT NOT NULL DEFAULT '',
  subagent_run_id TEXT REFERENCES fb_runs(id) ON DELETE SET NULL,
  sandbox_profile TEXT NOT NULL DEFAULT '',
  requested_profile TEXT NOT NULL DEFAULT '',
  profile_elevation INTEGER NOT NULL DEFAULT 0 CHECK (profile_elevation IN (0, 1)),
  resume_owner TEXT NOT NULL DEFAULT '',
  resume_claimed_at_ms INTEGER,
  resume_phase TEXT NOT NULL DEFAULT '' CHECK (resume_phase IN ('', 'claimed', 'execution_started', 'outcome_uncertain')),
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL
) STRICT;
CREATE INDEX idx_fb_run_waits_subagent_run ON fb_run_waits(subagent_run_id) WHERE subagent_run_id IS NOT NULL;

CREATE TABLE fb_files (
  id TEXT PRIMARY KEY,
  session_id TEXT NOT NULL REFERENCES fb_sessions(id) ON DELETE CASCADE,
  original_name TEXT NOT NULL,
  media_type TEXT NOT NULL,
  size_bytes INTEGER NOT NULL,
  sha256 TEXT NOT NULL,
  storage_backend TEXT NOT NULL CHECK (storage_backend IN ('local', 's3')),
  storage_bucket TEXT NOT NULL DEFAULT '',
  storage_key TEXT NOT NULL,
  parse_status TEXT NOT NULL CHECK (parse_status IN ('pending', 'running', 'done', 'failed')),
  parsed_text_path TEXT NOT NULL DEFAULT '',
  parse_error TEXT NOT NULL DEFAULT '',
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL
) STRICT;
CREATE INDEX idx_fb_files_session ON fb_files(session_id);

CREATE TABLE fb_cron_jobs (
  id TEXT PRIMARY KEY,
  agent_id TEXT NOT NULL CHECK (TRIM(agent_id) <> ''),
  project_id TEXT REFERENCES fb_projects(id) ON DELETE SET NULL,
  name TEXT NOT NULL DEFAULT '',
  schedule TEXT NOT NULL,
  prompt TEXT NOT NULL DEFAULT '',
  deliver TEXT NOT NULL DEFAULT '',
  enabled INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0, 1)),
  repeat_limit INTEGER NOT NULL DEFAULT 0,
  run_count INTEGER NOT NULL DEFAULT 0,
  next_run_at INTEGER,
  last_run_at INTEGER,
  last_status TEXT NOT NULL DEFAULT '' CHECK (last_status IN ('', 'ok', 'failed', 'running', 'delivery_failed')),
  last_error TEXT NOT NULL DEFAULT '',
  last_output TEXT NOT NULL DEFAULT '',
  failure_streak INTEGER NOT NULL DEFAULT 0,
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL
) STRICT;
CREATE INDEX idx_fb_cron_jobs_agent ON fb_cron_jobs(agent_id, created_at DESC);
CREATE INDEX idx_fb_cron_jobs_agent_project ON fb_cron_jobs(agent_id, project_id, created_at DESC);
CREATE INDEX idx_fb_cron_jobs_due ON fb_cron_jobs(agent_id, next_run_at) WHERE next_run_at IS NOT NULL;

-- Append-only fire history. It deliberately outlives the job and the session
-- it ran in, so job_id and session_id carry no foreign key.
CREATE TABLE fb_cron_runs (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  job_id TEXT NOT NULL,
  agent_id TEXT NOT NULL CHECK (TRIM(agent_id) <> ''),
  session_id TEXT NOT NULL,
  trigger TEXT NOT NULL CHECK (trigger IN ('schedule', 'manual')),
  status TEXT NOT NULL CHECK (status IN ('running', 'ok', 'failed', 'delivery_failed')),
  output TEXT NOT NULL DEFAULT '',
  error TEXT NOT NULL DEFAULT '',
  delivered_to TEXT NOT NULL DEFAULT '',
  started_at INTEGER NOT NULL,
  finished_at INTEGER
) STRICT;
CREATE INDEX idx_fb_cron_runs_job ON fb_cron_runs(job_id, started_at DESC);

-- next_run_at NULL means the heartbeat is paused.
CREATE TABLE fb_heartbeats (
  session_id TEXT PRIMARY KEY REFERENCES fb_sessions(id) ON DELETE CASCADE,
  interval_seconds INTEGER NOT NULL CHECK (interval_seconds > 0),
  prompt TEXT NOT NULL,
  next_run_at INTEGER,
  last_fired_at INTEGER,
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL
) STRICT;
CREATE INDEX idx_fb_heartbeats_due ON fb_heartbeats(next_run_at) WHERE next_run_at IS NOT NULL;

CREATE TABLE fb_memory_stage1_outputs (
  thread_id TEXT PRIMARY KEY,
  agent_id TEXT NOT NULL,
  project_key TEXT NOT NULL CHECK (TRIM(project_key) <> ''),
  source_updated_at INTEGER NOT NULL,
  raw_memory TEXT NOT NULL,
  rollout_summary TEXT NOT NULL,
  rollout_slug TEXT,
  generated_at INTEGER NOT NULL,
  usage_count INTEGER NOT NULL DEFAULT 0,
  last_usage INTEGER,
  selected_for_phase2 INTEGER NOT NULL DEFAULT 0 CHECK (selected_for_phase2 IN (0, 1)),
  selected_for_phase2_source_updated_at INTEGER,
  FOREIGN KEY (thread_id, agent_id) REFERENCES fb_sessions(id, agent_id) ON DELETE CASCADE
) STRICT;
CREATE INDEX idx_fb_memory_stage1_agent_project
  ON fb_memory_stage1_outputs(agent_id, project_key, source_updated_at DESC, thread_id DESC);

CREATE TABLE fb_memory_jobs (
  kind TEXT NOT NULL,
  job_key TEXT NOT NULL,
  agent_id TEXT NOT NULL CHECK (TRIM(agent_id) <> ''),
  status TEXT NOT NULL,
  ownership_token TEXT,
  started_at INTEGER,
  finished_at INTEGER,
  lease_until INTEGER,
  retry_at INTEGER,
  retry_remaining INTEGER NOT NULL,
  last_error TEXT,
  input_watermark INTEGER,
  last_success_watermark INTEGER,
  PRIMARY KEY (kind, job_key)
) STRICT, WITHOUT ROWID;
CREATE INDEX idx_fb_memory_jobs_agent_claim
  ON fb_memory_jobs(agent_id, kind, status, retry_at, lease_until);

-- first_rowid/line_count locate the file's rows in fb_memory_fts, so a stale
-- file is dropped by rowid range instead of a scan of the whole index.
CREATE TABLE fb_memory_index_files (
  root TEXT NOT NULL,
  path TEXT NOT NULL,
  size INTEGER NOT NULL,
  mtime_unix INTEGER NOT NULL,
  terms_revision INTEGER NOT NULL DEFAULT 0,
  first_rowid INTEGER NOT NULL,
  line_count INTEGER NOT NULL,
  PRIMARY KEY (root, path)
) STRICT, WITHOUT ROWID;

CREATE VIRTUAL TABLE fb_memory_fts USING fts5(
  terms,
  root UNINDEXED,
  path UNINDEXED,
  line_no UNINDEXED,
  tokenize = 'unicode61 remove_diacritics 2'
);
```

原 schema.sql 里对各表语义的解释性注释（projects、prompt_state、session_events、stage1、cron、
heartbeat、memory index、fts 的注释块）保留并按新字段名更新；描述「additive reconcile」和
`parseColumnDecls` 的注释（schema.sql 第 3–7 行）删除。

### 3.3 数量对比

| | 现在 | 目标 |
| --- | --- | --- |
| 状态库普通表 | 21（旧库另有残留 `fb_jobs`） | 16 |
| 触发器 | 2 | 0 |
| 二级索引 | 44 | 24 |
| `fb_sessions` 列 | 20 | 15 |
| `fb_messages` 列 | 18 | 16 |
| `fb_files` 列 | 28 | 14 |
| `fb_runs` 列 | 13 | 15 |
| `fb_session_events` 列 | 8 | 7 |
| `fb_heartbeats` 列 | 9 | 7 |
| `fb_cron_runs` 列 | 12 | 11 |
| `fb_run_waits` 列 | 8 | 16 |

`fb_runs` 列数增加是把原来在每一条消息行上重复存放的 run 计时收回到 run 自己身上（T4）。
`fb_run_waits` 列数增加是有意的：把 JSON 里的 9 个被 CAS 读写的字段提成列，才能用单条条件 UPDATE
取代「读整段 JSON→比较整段」的 8 次重试循环（R8/S9），并让租约字段互不冲突。

### 3.4 history.db 最终形态（`pkg/tool/loaded_skills.go`）

```sql
CREATE TABLE output_records (
  id INTEGER PRIMARY KEY,
  kind TEXT NOT NULL CHECK (kind IN ('shell', 'mcp')),
  command TEXT NOT NULL,
  session_id TEXT NOT NULL DEFAULT '',
  original_output TEXT NOT NULL,
  filtered_output TEXT NOT NULL,
  original_output_bytes INTEGER NOT NULL,
  filtered_output_bytes INTEGER NOT NULL,
  saved_tokens INTEGER NOT NULL,
  capability_id TEXT NOT NULL DEFAULT '',
  capability_version TEXT NOT NULL DEFAULT '',
  retrieve_count INTEGER NOT NULL DEFAULT 0,
  retrieve_reason TEXT NOT NULL DEFAULT '',
  spool_path TEXT NOT NULL DEFAULT '',
  spool_omitted_bytes INTEGER NOT NULL DEFAULT 0,
  created_at_ms INTEGER NOT NULL
) STRICT;
```

该库与状态库无关联（不同文件），不加 FK；两个旧索引删除（无对应查询）。

---

## 4. 迁移设计

### 4.1 框架（T1 建立，后续任务只扩展）

新文件 `pkg/state/schema_migrations.go`（**不要**放进 `pkg/migrate`——那是「从 Claude Code/Codex
导入历史」的功能包，名字相同语义不同）。

```go
// stateSchemaVersion is the shape schema.sql declares. A database at a lower
// version is carried forward by the migrations below; one at a higher version
// was written by a newer binary.
const stateSchemaVersion = 1

type schemaMigration struct {
	version int
	apply   func(ctx context.Context, tx *sql.Conn) error
}

var stateSchemaMigrations = []schemaMigration{
	{version: 1, apply: migrateStateV0ToV1},
}
```

`Open`（非只读）的流程替换 `applyStateSchema` + `normalizeSessions`：

1. `conn, _ := db.Conn(ctx)`；`PRAGMA busy_timeout = 600000`（只作用于这个连接：另一进程正在迁移
   800MB 的库时，本进程等它完成而不是 5 秒后报错）。
2. `PRAGMA foreign_keys = OFF`（必须在事务外执行，事务内设置无效）。
3. `BEGIN IMMEDIATE`；**在事务内**读 `PRAGMA user_version`（拿到写锁后再读，另一进程刚迁完就直接
   提交返回）。
4. `user_version == stateSchemaVersion` → COMMIT，恢复 `foreign_keys = ON`，返回。
   `user_version > stateSchemaVersion` → ROLLBACK，返回 `ErrStateSchemaNewer`
   （错误文本一句话：`state database was written by a newer forebrain; upgrade forebrain to open it`）。
5. 空库（`sqlite_master` 里没有任何 `fb_` 表）→ 执行 `schemaSQL`，设 `user_version`，COMMIT。
6. 否则依次执行 `version > user_version` 的每个迁移；全部执行完：
   - `PRAGMA foreign_key_check` 必须返回 0 行，否则 ROLLBACK 并返回错误（错误文本列出违例表名与行数）；
   - `PRAGMA user_version = stateSchemaVersion`；COMMIT。
7. 恢复 `PRAGMA foreign_keys = ON`；关闭 conn。
8. 迁移确实执行过（不是第 4、5 步）时，在事务外执行一次 `VACUUM` 回收删表后的空间，并用
   `slog.Info` 记录「migrated state database from vN to vM」及各表迁移/丢弃行数。

只读打开（`OpenOptions.ReadOnly`）不迁移：读 `user_version`，不等于 `stateSchemaVersion` 时返回
`ErrStateSchemaOutdated`（一句话：`state database needs an upgrade; start forebrain once to upgrade it`）。

DSN（`sqliteDataSourceForStateFile`，`pkg/state/db.go:397-416`）改为
`mode=rwc&_busy_timeout=5000&_journal_mode=WAL&_synchronous=NORMAL&_foreign_keys=1&_txlock=immediate`。
`_synchronous=NORMAL` 不改变现有行为：go-sqlite3 默认就在每个连接上执行 `PRAGMA synchronous = NORMAL`
（`github.com/mattn/go-sqlite3@v1.14.44/sqlite3.go:1205`、`:1862`），这里把它写明，让持久性级别成为显式选择——
进程崩溃不丢已提交事务；断电/系统崩溃可能丢最后几个已提交事务，但不会损坏库。
原注释（关于不用 `cache=shared`）保留。

### 4.2 `migrateStateV0ToV1` 的骨架（T1 建立）

v0 = 当前 schema.sql 以及更早二进制写出的任意形态（列可能更少、可能有残留表）。步骤：

1. `DROP TRIGGER IF EXISTS` 两个 `fb_tui_messages_touch_session_*` 触发器，以及 `sqlite_master` 里
   其它所有 `type='trigger'` 的对象。
2. `DROP INDEX` `sqlite_master` 里所有 `type='index' AND sql IS NOT NULL` 的索引（自动索引 `sql`
   为 NULL，随表删除）。不先删，新 schema 的同名 `CREATE INDEX` 会撞名。
3. 把每张现存的 `fb_` 普通表（排除 FTS 虚表及其 `fb_memory_fts_*` 影子表）
   `ALTER TABLE x RENAME TO legacy_x`。
4. `DROP TABLE fb_memory_fts`（派生数据，可重建；它的影子表随之消失）。
5. 执行最终 `schemaSQL`（与空库完全相同的文本，保证 `sqlite_master` 一致）。
6. 按依赖顺序对每张目标表调用它的拷贝函数：
   `projects → sessions → runs → messages → session_ui_state → session_prompt_state →
   session_events → actions → run_waits → files → cron_jobs → cron_runs → heartbeats →
   memory_stage1_outputs → memory_jobs`（`fb_memory_index_files` 不拷贝：FTS 已删，索引记录必须
   一起清空，下次搜索自动重建——这是派生数据的正确迁移）。
7. 拷贝完后 `DROP TABLE` 所有 `legacy_*`，以及 `sqlite_master` 中**不在最终 schema 里**的任何
   普通表（`fb_jobs` 等残留）。「最终 schema 里有哪些对象」通过在 `:memory:` 上执行 `schemaSQL`
   后读 `sqlite_master` 得到（复用现 `declaredSchemaShape` 的思路，但只取对象名集合）。

拷贝函数的通用形态：T1 先提供一个**通用拷贝** `copyCommonColumns(ctx, conn, table)`：取
`PRAGMA table_info(legacy_x)` 与新表列的交集，`INSERT INTO x (cols) SELECT cols FROM legacy_x`；
新表有而旧表没有的列，若新列有 DEFAULT 则省略（由默认值填充），若是 NOT NULL 无默认则返回错误
（说明该表需要专用拷贝函数）。T2–T8 改哪张表，就把那张表从通用拷贝换成专用拷贝函数
（`migrateV1Sessions`、`migrateV1Messages`……），专用函数里用 `legacyColumnExpr(conn,
"legacy_x", "col", "<缺列时的默认 SQL 表达式>")` 处理「更老的旧库可能没有这一列」。

### 4.3 各表的 v1 数据转换（各任务实现自己负责的行）

| 目标表 | 来源与转换 | 丢弃（写入日志计数） |
| --- | --- | --- |
| `fb_projects` | `archived_at`: `NULLIF(archived_at,0)` | — |
| `fb_sessions` | `title`: `CASE WHEN TRIM(IFNULL(title,''))='' THEN id ELSE title END`；`parent_session_id`/`project_id`: `NULLIF(x,'')`，且目标不存在时置 NULL（悬空引用）；`origin`: `CASE WHEN origin='migrated' THEN 'migrated' ELSE 'native' END`；`compact_boundary_message_id`: `CAST(NULLIF(compact_boundary_id,'') AS INTEGER)`，指向的消息不存在则 NULL；`context_reset_message_id`: `NULLIF(context_reset_row_id,0)`，同上；`created_at`: 旧值 ≤0 时取该会话最小消息 `created_at`，仍无则取 `updated_at`；`updated_at`: `MAX(旧 updated_at, created_at)` | `agent_id` 为空的行（违反现有 CHECK，理论上不存在） |
| `fb_runs` | `parent_run_id`: `NULLIF(parent_run_id,'')`；丢 `token_estimate`；计时三列由 `migrateV1RunTiming` 在消息拷贝后写入，旧回合匹配不到 run 时补建 run 行（规则见 T4 第 3 步） | `session_id` 不在 `fb_sessions` 的行 |
| `fb_messages` | `visibility`: `transcript→visible`、`transcript_repaired→repaired`、`transcript_withdrawn→withdrawn`；`run_id`: `NULLIF(run_id,'')`，run 不存在则 NULL；`model`: `IFNULL(model,'')`；计时：tool 行与 `!cmd` 用户行转成 `exec_*` 三列（毫秒），其余行的计时交给 `migrateV1RunTiming` 写入 `fb_runs`，旧行 `run_id` 按 T4 第 3 步回填；丢 `updated_at`、`finished_at` | `source` 为其它值（如历史 `'tui'`）的行——所有读取方都只读 `transcript`，这些行本就不可达；session 不存在的行；`role` 不在枚举内的行 |
| `fb_session_ui_state` / `fb_session_prompt_state` | 原样拷贝（**`value` 逐字节不变**，这是缓存前缀） | session 不存在的行 |
| `fb_session_events` | 见 §4.4 | 见 §4.4 |
| `fb_actions` | `session_id`: `payload_json->>'$.session_id'`；为空时取 `fb_run_waits(action_id)→fb_runs.session_id`；`answer_json`/`error`: `IFNULL(x,'')` | 两路都取不到会话、或会话不存在的 action（租户过滤后本就不可见） |
| `fb_run_waits` | 每个 `run_id` 只保留一行：按 `CASE WHEN tool_name='long_run_async_switch' THEN 0 ELSE 1 END, updated_at DESC` 取第一行（与现 `GetWaitForRun` 选择规则一致）；`source_json` 拆列：`agent_id`=`->>'$.agent_id'`、`subagent_type`、`subagent_run_id`（run 不存在则 NULL）、`sandbox_profile`、`requested_profile`、`profile_elevation`=`IFNULL(->>'$.profile_elevation',0)`（JSON true→1）、`resume_owner`、`resume_claimed_at_ms`=`NULLIF(->>'$.resume_claimed_at',0)`、`resume_phase` | run 或 action 不存在的行；同 run 的多余行 |
| `fb_files` | `storage_key`: `CASE storage_backend WHEN 's3' THEN oss_key ELSE storage_relpath END`；`storage_bucket`: `oss_bucket`；`parsed_text_path`: `parsed_text_relpath`；`parse_error`: `error` | `source<>'attachment'` 的行（`workspace_file` 无写入方）；session 不存在的行 |
| `fb_cron_jobs` | `project_id`: `NULLIF(project_id,'')`，项目不存在则 NULL；`last_run_at`: `NULLIF(last_run_at,0)` | — |
| `fb_cron_runs` | `finished_at`: `NULLIF(finished_at,0)`；`agent_id` 为空时取 `fb_cron_jobs.agent_id`；丢 `run_id` | 仍取不到 agent 的行 |
| `fb_heartbeats` | `next_run_at`: `CASE WHEN paused=1 OR next_run_at<=0 THEN NULL ELSE next_run_at END`；`last_fired_at`: `NULLIF(last_fired_at,0)`；丢 `paused`、`agent_id` | session 不存在的行 |
| `fb_memory_stage1_outputs` | `usage_count`: `IFNULL(usage_count,0)`；`selected_for_phase2`: `CASE WHEN selected_for_phase2<>0 THEN 1 ELSE 0 END` | `(thread_id, agent_id)` 与 `fb_sessions` 不匹配的行 |
| `fb_memory_jobs` | 丢 `worker_id` | — |
| `fb_memory_index_files` | 不拷贝（见 §4.2 第 6 步） | 全部（派生数据，下次搜索重建） |
| `fb_tool_audit` `fb_work_items` `fb_todos` `fb_intel_audit` `fb_jobs` | — | 整表删除（tool_audit 的内容由工具事件表达，§2.2） |

### 4.4 事件日志合并的数据迁移（T5 实现）

合并后 `fb_session_events` 必须满足：**每个事实只记一次**；已有事件的相对顺序不变；从步骤迁来的
事件按时间插入到正确位置。

**哪些旧步骤要迁**（其余步骤在 v0 里都有规范事件孪生或无人读取，随表丢弃）：

| 旧步骤（`fb_run_steps.event_type`） | 条件 | 迁成 |
| --- | --- | --- |
| `tool_call_started` / `tool_call_completed` | 主代理（`payload->>'$.agent_id'` 为空或 NULL，且 run 不是子代理 run）；`payload->>'$.suppress_ui'` 不为真；`tool_name <> 'request_permissions'`；不是「以计划卡呈现」的工具（名单照抄 `event.ToolStepRendersAsPlan`，见 T5 步骤 4.8）；同会话里**不存在** `event_type` 相同、`payload->>'$.step_id'` 相同的事件 | 同名事件，payload 重塑为规范 `ToolCallStartedPayload`/`ToolCallCompletedPayload` 的 JSON（§4.4.1） |
| `plan_updated` | `payload->'$.plan_update'` 非空，且其 `agent_id` 为空 | `plan_updated`，payload = `payload->'$.plan_update'` |

每条迁来事件：`session_id` = `fb_runs.session_id`（run 不存在则丢弃）；`run_id` = 步骤的 run_id；
`event_id` = `'evt-' || run_id || '-' || seq`（与现 `turn.ProjectRunEvents` 的 `baseID` 相同，
`pkg/turn/events.go:129`）；`occurred_at_ms` = `created_at * 1000`。

**排序**：旧 `sequence` 是权威的会话游标（schema 注释：它不依赖挂钟排序父/子/后台事件），不能按时间
重排已有事件。在 Go 里做一次线性归并：游标 A 按 `sequence` 读旧事件，游标 B 按
`(created_at, run_id, seq)` 读待迁步骤；每次取 A 的下一条，除非 B 的下一条 `occurred_at_ms` 严格
小于 A 的下一条——那就先放 B。按归并顺序逐行 `INSERT`（不指定 `sequence`，由 AUTOINCREMENT 重新
编号）。游标重编号是安全的：代码库里没有持久化的事件游标（TUI 浏览状态只存滚动位置，
`pkg/tui/render.go:468-478`；web 游标只在页面内存，`frontend/src/composables/useChatStream.ts:1363`），
升级后客户端重连即重新同步。

**旧事件的转换**：`run_id` = `NULLIF(run_id,'')`（run 不存在则 NULL）；丢 `schema_version`；
session 不存在的旧事件丢弃（本机 5 行，都是 D16 的 MCP 启动失败孤儿）。

#### 4.4.1 工具步骤 payload 重塑（SQL 表达式模板，`p` 为旧 `payload_json`）

```sql
json_object(
  'kind', p->>'$.kind',
  'step_id', p->>'$.step_id',
  'tool_name', p->>'$.tool_name',
  'description', IFNULL(p->>'$.tool_description', ''),
  'error', IFNULL(p->>'$.error', ''),
  'action_id', IFNULL(p->>'$.action_id', ''),
  'action_kind', IFNULL(p->>'$.action_kind', ''),
  'duration_seconds', IFNULL(p->>'$.duration', 0) / 1e9,
  'output', json(IFNULL(p->'$.output', '{}')),
  'tool_meta', json_object('tool_name', p->>'$.tool_name', 'input', json(IFNULL(p->'$.input', '{}')))
)
```

（`time.Duration` 的 JSON 是纳秒整数。started 事件省略 `output`/`error`/`duration_seconds`。
`error_type` 不在 SQL 里算：读取方需要时调用 `event.ClassifyToolError(error)`。）

### 4.5 迁移验收（每个任务都要跑，T11 用真实库演练）

1. **结构一致**：`TestStateMigrationMatchesFreshSchema`——用 `pkg/state/testdata/legacy_v0_schema.sql`
   （T1 时从**当前** schema.sql 原样复制，此后永不修改）+ `legacy_v0_fixture.sql`（各任务追加
   覆盖本任务转换规则的行）建旧库，`Open` 之后：`user_version = 1`；`SELECT type, name, tbl_name,
   sql FROM sqlite_master WHERE name NOT LIKE 'sqlite_%' ORDER BY type, name` 与空库 `Open` 的结果
   **完全相等**。
2. **FK 完整**：迁移后 `PRAGMA foreign_key_check` 返回 0 行。
3. **数据保真**：每个专用拷贝函数配一个断言测试（本任务的转换表逐行覆盖）。
4. **缓存不变（最高优先级）**：`TestStateMigrationPreservesModelContextBytes`——迁移前后
   对 fixture 中每个会话，所有可见行的 `(id, role, content, parts)` 与所有
   `fb_session_prompt_state (session_id, key, value)` 逐字节相等。T11 另在真实库副本上验证
   `ListTranscriptMessages` 输出（附录 B）。
5. **幂等**：对已是 v1 的库再次 `Open`，不执行任何 DDL（用 `sqlite3_changes`/`total_changes` 或
   记录 `sqlite_master` 前后相同来断言）。

---

## 5. 常用命令

| 用途 | 命令 | 成功时 |
| --- | --- | --- |
| 全量测试 | `CGO_ENABLED=1 go test -tags fts5 ./... -count=1` | exit 0 |
| 单包测试 | `CGO_ENABLED=1 go test -tags fts5 ./pkg/state -count=1 -run <Name>` | `ok` |
| vet | `go vet ./...`；另跑 `GOOS=linux go vet ./...`、`GOOS=windows go vet ./...` | exit 0 |
| 包依赖图 | `scripts/package-graph.sh`，然后 `git status --porcelain pkg/architecture/testdata/graph.json` | 无输出（本计划不新增/删除包、不改包间 import 时图不变；若确有变化，重新生成并在报告里说明） |
| 死代码 | `$(go env GOPATH)/bin/deadcode ./... > /tmp/deadcode.txt` | 本计划相关条目为 0 |
| 构建 | `go build -o ./build/bin/forebrain ./cmd/forebrain` | exit 0 |
| 前端 | `cd frontend && pnpm install && pnpm build` | exit 0（T3/T5/T6 改前端类型后必须跑） |
| 查某库结构 | `sqlite3 "file:<path>?immutable=1" ".schema"` | — |

实机验证技能：TUI 用 `run-forebrain` 技能（构建、启动、发按键、读屏、查状态库）；web 用 `run` 技能
启动 gateway 并驱动浏览器。两者都必须用临时 home（环境变量 `FOREBRAIN_HOME`，`run-forebrain` 技能默认就用隔离 home），
不得指向 `~/.forebrain`。

## 6. 范围与边界

**范围内**：`pkg/state/**`、`pkg/memory/{store.go,jobs.go,ftsindex.go}` 及其测试、`pkg/migrate/sessions.go`、
`pkg/tool/loaded_skills.go`（history.db 部分）、`pkg/tool/{notification.go,state.go}` 中与步骤/事件
相关的函数、`pkg/turn/{events.go,scheduler.go,approval.go,session.go,context.go}`、
`pkg/run/{goal.go,subagent.go,telemetry.go,runner.go}`、`pkg/process/cron_service.go`、
`pkg/tui/{chat_turn.go,notify.go,chat_surface.go,chat_session.go,commands.go}`、
`pkg/gateway/{api_extra.go,server.go,approval.go,run_control.go,agents_api.go,serve_run.go,wsevents.go}`、
`pkg/testutil/scenario.go`、`frontend/src/lib/api.ts`、`frontend/src/views/ChatView.vue`
（仅限本计划列出的字段/类型/接口调整），以及上述文件的 `_test.go`。

**范围外（不要碰）**：
- prompt 组装（`pkg/assembly`、`internal/agentrun/anthropic_prompt_cache.go` 等）——本计划不得改变任何
  发给模型的字节。
- `pkg/migrate` 的导入逻辑本身（只改它写入 `fb_sessions`/`fb_messages` 的列清单）。
- 事件的**线上协议**（`event.RunEvent` 的 JSON 形状、websocket op 名）——前端依赖它。
- `fb_session_events` 里子代理流式片段的逐条持久化——**owner 2026-09-27 裁决：维持现状，不压缩**。事实记录：本机 62148 行事件中 56724 行是 `assistant_delta`/`reasoning_delta`，全部属于子代理，是子代理视图回放的唯一文字来源（`pkg/tui/commands.go:786-797`）；逐片落库保证了输出中途崩溃不丢已输出文字、web 输出中途断线可按序号续读、子代理视图实时与回放一致。执行者不得改动其写入方式。
- `SQLITE synchronous` 级别：驱动默认已是 WAL 下最快的安全级别 `NORMAL`，没有可调的性能空间（`OFF` 有损坏风险）；
  本计划只在 DSN 里把它显式写出（T1），不改变行为。

## 7. 实施任务

> 通用要求：每个任务结束时都要满足——全量测试通过、`go vet` 通过、§4.5 的 1–3（及涉及缓存字节的
> 任务的 4）通过、`deadcode` 输出里不再有本任务造成的无调用方函数、`git status` 只出现范围内文件。
> **任何任务都不 commit。**

### T0 基线

1. 跑全量测试，把失败列表存到 `/tmp/fb_baseline_tests.txt`。
   **Verify**：文件存在。若已有失败，按仓库规则这些失败也要在本计划内修到根上——先 STOP 报告失败
   清单，由 owner 决定是否并入。
2. 跑附录 A 的基准脚本，把输出存到 `/tmp/fb_baseline_bench.txt`。
   **Verify**：包含 `SessionIDWithLatestMessage` 等 8 行耗时。
3. 复制真实库做演练素材（只读复制，不打开原库写入）：
   `mkdir -p /tmp/fb_rehearsal && sqlite3 "file:$HOME/.forebrain/state/forebrain.state.sqlite?immutable=1" ".backup /tmp/fb_rehearsal/pristine.sqlite"`，
   同样处理 `~/.forebrain/workspace/state/outfilter/history.db`（若存在）→ `/tmp/fb_rehearsal/pristine_history.db`。
   **Verify**：`sqlite3 /tmp/fb_rehearsal/pristine.sqlite "select count(*) from fb_messages"` 输出与原库一致。
4. 在**改代码之前**，按附录 B 生成缓存基线：对 pristine 库每个会话导出 `ListTranscriptMessages`
   的 JSON 与所有 prompt_state，算 sha256，存 `/tmp/fb_rehearsal/context_before.txt`。
   **Verify**：文件行数 = 会话数。

### T1 迁移框架、DSN、删除死表与开库修补

**目的**：建立 §4.1/§4.2 的版本化迁移；删掉加法补列、开库归一化、`'tui'` 触发器、3 张死表；
DSN 开启 `_foreign_keys=1`、`_txlock=immediate`（修 D8/R1）。

**步骤**
1. 复制当前 `pkg/state/schema.sql` 为 `pkg/state/testdata/legacy_v0_schema.sql`（此后永不修改），
   新建 `pkg/state/testdata/legacy_v0_fixture.sql`，先放：3 个会话（其中一个 `title` 为 NULL）、
   每会话若干消息（含 `source='tui'` 一行）、一张残留表 `CREATE TABLE fb_jobs(id TEXT)` 与一行数据。
2. schema.sql：删除 `fb_work_items`、`fb_todos`、`fb_intel_audit` 三张表及其索引；删除两个触发器；
   删除首行 `PRAGMA journal_mode = WAL;`（理由见 §3.2）；所有 `CREATE … IF NOT EXISTS` 改为 `CREATE …`；
   删除第 3–7 行关于 additive reconcile 的注释。
   其余表此时保持原样（后续任务再改）。
3. 新建 `pkg/state/schema_migrations.go`，实现 §4.1 的 `migrateStateSchema` 与 §4.2 的
   `migrateStateV0ToV1` 骨架 + `copyCommonColumns` + `legacyColumnExpr`；`Open`（`db.go:20-50`）
   改为调用它；只读分支按 §4.1 处理。
4. 删除 `db.go` 中 `applyStateSchema`、`tableShape`、`columnDecl`、`declaredSchemaShape`（对象名集合
   的计算改写进 schema_migrations.go，只保留需要的部分）、`isVirtualTableDDL`、`isShadowTable`、
   `addColumnsMissingFrom`、`parseColumnDecls`、`columnsFromEntries`、`leadingIdentifier`、
   `isTableConstraintKeyword`、`sqliteTableColumns`、`normalizeSessions`、`normalizeSessionsTx`，
   以及 `db_test.go` 里测试它们的用例。
5. DSN 按 §4.1 修改；`sqliteDataSourceForStateFileReadOnly` 同步加 `_foreign_keys=1`（只读无影响，
   保持一致）。
6. `pkg/memory/store.go:470-476`、`pkg/memory/jobs.go:339-345` 的 `db.Conn` + 手写 `BEGIN IMMEDIATE`
   改为普通 `s.DB.BeginTx(ctx, nil)`（DSN 已使其为 IMMEDIATE），删除随之失去用途的 conn 处理代码。
7. 前端 `frontend/src/lib/api.ts:670` 的 `TaskItemRecord`（`fb_work_items` 残留类型）删除。
8. 写测试：§4.5 第 1、2、5 条；另测 `user_version` 大于当前时返回 `ErrStateSchemaNewer`；
   只读打开 v0 库返回 `ErrStateSchemaOutdated`；fixture 的 `fb_jobs` 迁移后不存在；
   `source='tui'` 行此时仍按原样通过通用拷贝（T2 才丢弃）。

**Verify**
- `CGO_ENABLED=1 go test -tags fts5 ./pkg/state ./pkg/memory -count=1` → ok
- `grep -rn "addColumnsMissingFrom\|normalizeSessions\|fb_tui_messages_touch_session\|fb_work_items\|fb_intel_audit\|fb_todos" pkg frontend/src --include='*.go' --include='*.sql' --include='*.ts'`
  → 只允许命中 `pkg/state/testdata/legacy_v0_schema.sql` 与迁移代码里的字面表名
- `grep -n "_synchronous=NORMAL&_foreign_keys=1&_txlock=immediate" pkg/state/db.go` → 1 行
- 全量测试通过

**STOP**：`ALTER TABLE … RENAME` 因为某个旧对象引用报错且不属于 §4.2 列出的对象类型。

### T2 会话、消息、项目、UI/prompt 状态表

**目的**：§3.2 中 `fb_projects`、`fb_sessions`、`fb_messages`、`fb_session_ui_state`、
`fb_session_prompt_state` 落到最终形态（`fb_messages` 的计时三列此任务**先原样保留**旧列
`run_started_at TEXT`、`run_finished_at TEXT`、`worked_duration_ms INTEGER`，由 T4 规范化为 `exec_*` 并把 run 计时迁到
`fb_runs`）；修 S1、S10–S15、S19、S24、R2/D6、R3/D7、R7。

**步骤**
1. schema.sql 写入上述 5 张表的最终 DDL，但 `fb_messages` 暂不加 `exec_*` 三列及其两个 CHECK，改为保留
   `run_started_at TEXT NOT NULL DEFAULT ''`、`run_finished_at TEXT NOT NULL DEFAULT ''`、`worked_duration_ms INTEGER NOT NULL DEFAULT 0`（T4 再换）。
2. 迁移：实现 `migrateV1Projects`、`migrateV1Sessions`、`migrateV1Messages`、`migrateV1SessionUIState`、
   `migrateV1SessionPromptState`，严格按 §4.3。fixture 追加覆盖每条转换与每条丢弃规则的行。
3. 引入 `dbtx` 接口（`ExecContext/QueryContext/QueryRowContext`，`*sql.DB` 与 `*sql.Tx` 都满足），
   SessionStore 的内部写函数改为接收 `dbtx`。
4. **消息写入合并**（S10–S12）：新增唯一的 `insertMessage(ctx, q dbtx, row messageRow) (int64, error)`
   —— 一条 INSERT 覆盖全部列。`AppendMessageWithSourceForRun`、`AppendStructuredMessageForRun`、
   `AppendCompactCheckpoint`（`session_store.go:1657`）都改用它。每个 append 的完整流程在**一个事务**里：
   `ensureSession(q)`（现 `Ensure` 的 upsert，`session_store.go:126-139`）→ `insertMessage` →
   若 `role='user' AND visibility='visible'` 且内容非空：
   `UPDATE fb_sessions SET title=?, updated_at=? WHERE id=? AND agent_id=? AND title=id`（R3/D7）。
   删除 `touchSessionFromTurn`、`shouldGenerateTitleForSession`，以及 append 路径里多余的 `Ensure` 调用。
5. **source 参数删除**：所有调用方传的都是 `"transcript"`（`grep -rn "AppendWithSource\|AppendMessageWithSource\|AppendStructuredMessage" pkg --include='*.go'` 核对）。
   从 `AppendWithSource`/`AppendMessageWithSource`/`AppendMessageWithSourceForRun`/
   `AppendStructuredMessage`/`AppendStructuredMessageForRun` 及 `pkg/turn/session.go` 的接口
   （`:220`、`:290`、`:375`）中删掉 `source` 参数；只剩一个调用方的包装函数合并，`deadcode` 报的全部删除。
   写入值固定 `visibility='visible'`；`setMessageSource` 改名 `setMessageVisibility`，常量改为
   `messageVisible/messageRepaired/messageWithdrawn`（`session_store.go:1328-1331`）。
6. **读取合并**（S13、S14、S15）：定义 `const messageColumns = "id, run_id, role, content, message_id, parts, model, created_at, usage_json, tool_step_id, tool_meta_json, run_started_at, run_finished_at, worked_duration_ms"`
   与 `scanMessage(scanner) (storedMessage, error)`，替换 `session_store.go:632/826/937/960` 四处；
   所有查询里的 `IFNULL(source,'transcript')='transcript'` 改为 `visibility='visible'`，
   并删掉对 NOT NULL 列的 `IFNULL`；`LastTranscriptRowID` 与 `LatestTranscriptRowID` 合并为一个
   （保留 `requireOwned`），删除 `LatestContentBySource`。
   `run_id` 读取：`sql.NullString`→`string`（NULL 读成 ""）。
7. **删列**：从 `storedMessage`/`Message` 删 `FinishedAt`（及 `nullableFinishedAt`），编译；若编译错误出现在
   `pkg/state` 与测试之外（说明有真实读取方）→ STOP。`updated_at` 同样处理（写入它的
   `UpdateMessageParts`/`setMessageVisibility` 不再写）。`model` 列保留（`usage.go` 等处有读取）。
   `fb_sessions` 删列对应的 `pkg/migrate/sessions.go:330-338` 列清单同步删除
   `message_count, prompt_tokens, completion_tokens, cost`，`origin` 写 `'migrated'`；
   `insertRows`（`:353-360`）删 `updated_at, finished_at`、`source` 改 `visibility`。
   `parsedSession` 里随之无人读的字段（`PromptTok`、`CompletionTok`、`CostUSD` 等）按 deadcode 删除。
8. **会话列改造**：`compact_boundary_id`→`compact_boundary_message_id INTEGER`（`AppendCompactCheckpoint`
   `:1667`、`compactBoundaryRowID` `:1906-1935` 去掉字符串解析）、`context_reset_row_id`→
   `context_reset_message_id`（`:1798-1860`，0 改 NULL）、`parent_session_id`/`project_id` 用 NULL；
   `ForkInto`（`:1682-1776`）的列清单同步；`DeleteSessionMessages`（`:1777-1797`）不再需要手动清
   边界指针（FK `SET NULL`）——若它现在有手动清理语句则删除。`SessionTitle`/列表查询去掉
   `IFNULL(title,'')`。
9. **S1**：`SessionIDWithLatestMessage` 改为
   ```sql
   SELECT s.id FROM fb_sessions s
   WHERE s.agent_id = ?
     AND EXISTS (SELECT 1 FROM fb_messages m WHERE m.session_id = s.id AND m.visibility = 'visible')
   ORDER BY s.updated_at DESC, s.id DESC LIMIT 1
   ```
   写特征测试：旧实现与新实现在 fixture 上返回同一会话（在删除旧实现前先写好并通过）。
10. **R2/D6**：`AppendMessageSequenceForRun`（`message_sync.go:34-63`）把「读 transcript →
    `planTranscriptAppend` → 补全 → 追加」放进一个事务（`listTranscriptStoredMessages` 需要能接收 `dbtx`）。
    `AppendNewMessages` 同样放进一个事务。
11. **R7**：`RepairDanglingToolResults` 的读与所有修复写入放进一个事务。
12. **S19**：`project_store.go:284` 的 `UPDATE fb_sessions SET project_id=''…` 删除（FK `SET NULL`），
    确认删除项目的事务只剩 `DELETE FROM fb_projects`。`archived_at` 相关查询：`archived_at=0`→
    `archived_at IS NULL`（`project_store.go:318`），`ORDER BY archived_at ASC` 保持（NULL 排最前，
    与原 0 的语义一致）；`Project.ArchivedAt` 改 `sql.NullInt64` 或 `*int64`（与该文件现有风格一致）。
13. 测试：
    - 并发测试：两个 goroutine 对同一会话同时调用 `AppendMessageSequenceForRun`（同一消息列表），
      结束后可见行数 = 列表长度（修复前应能复现重复——先写测试确认它在旧代码上失败）。
    - 并发命名测试：两条用户消息并发写入，会话标题 = 其中一条生成的标题，不是会话 id。
    - §4.5 第 4 条（缓存字节）。

**Verify**
- `grep -rn "IFNULL(source\|'transcript_repaired'\|'transcript_withdrawn'\|source = 'transcript'\|source='transcript'" pkg --include='*.go'` → 0 行（迁移代码里的旧值映射除外）
- `grep -rn "message_count\|prompt_tokens, completion_tokens, cost\|compact_boundary_id\b\|context_reset_row_id" pkg --include='*.go' --include='*.sql'` → 只命中 testdata 与迁移映射
- `CGO_ENABLED=1 go test -tags fts5 ./pkg/state ./pkg/migrate ./pkg/turn ./pkg/tui ./pkg/gateway -count=1` → ok

**STOP**：第 7 步编译错误出现在 `pkg/state` 之外；第 9 步特征测试新旧结果不一致且差异无法由
「空会话行」解释；任一 transcript 读取在新旧实现下返回的行集合不同。

### T3 runs、run_waits、actions

**目的**：三张表落最终形态；修 S3–S6、S8、S9、S22、R8、D13、D14、D17。

**步骤**
1. schema.sql 写三表最终 DDL；迁移 `migrateV1Runs`、`migrateV1Actions`、`migrateV1RunWaits`（§4.3）；fixture 覆盖：
   同 run 两条 wait、payload 无 session 但 wait→run 能反查的 action、两路都查不到会话的 action、
   `source_json` 含全部 9 个键的 wait。
2. `fb_runs`：删 `token_estimate`（`Run.TokenEstimate`、`CreateRun`/`CreateSubagentRun` 的计算与写入）；
   `parent_run_id` 用 NULL（`Run.ParentRunID` 仍为 string，NULL↔""），
   `LastRunUsageForSession`（`runrt.go:393`）`parent_run_id=''`→`parent_run_id IS NULL`，
   `setRunningDescendantsStatus`/递归 CTE 不变（有了索引）。
3. **D17**：`CreateSubagentRun`（`runrt.go:228-266`）删掉 `sid == "" → "default"` 分支，空会话 id 返回错误；
   用 `grep -rn "CreateSubagentRun(" pkg --include='*.go'` 核对每个调用方都传入真实会话；有传空的 → STOP。
4. **S5/D13**：删除 `ListRunsRecent`；新增
   `ActivePrimaryRun(ctx, agentID) (Run, bool, error)`：
   ```sql
   SELECT <run columns> FROM fb_runs r JOIN fb_sessions s ON s.id = r.session_id
   WHERE s.agent_id = ? AND r.parent_run_id IS NULL AND r.status IN ('running', 'waiting_action')
   ORDER BY r.updated_at DESC LIMIT 1
   ```
   `findCancellablePrimaryRun`（`pkg/gateway/agents_api.go:369-389`）改用它，agentID 用该 Server 当前
   primary agent（与同文件其它租户过滤取值方式一致）。
5. **fb_run_waits 类型化**（S9/R8/S22）：`Wait` 结构不变；删除 `marshalWaitSource`/`parseWaitSource`；
   `SetWaitingAction`（`runrt.go:471-506`）的 upsert 按 `run_id` 冲突、写全部列，删掉 `DELETE … action_id<>?`
   （主键保证一 run 一行）。五个租约函数改为**单条**条件 UPDATE，无预读、无循环：
   - `claimWaitResume(owner, refreshSame)`：
     ```sql
     UPDATE fb_run_waits
     SET resume_owner = :owner, resume_claimed_at_ms = :now_ms,
         resume_phase = CASE WHEN resume_phase = '' THEN 'claimed' ELSE resume_phase END,
         updated_at = :now
     WHERE run_id = :run AND action_id = :action
       AND (
         (:refresh = 1 AND resume_owner = :owner)
         OR (resume_phase NOT IN ('execution_started', 'outcome_uncertain')
             AND (resume_owner = '' OR resume_claimed_at_ms IS NULL OR resume_claimed_at_ms < :stale_before_ms))
       )
     RETURNING resume_owner
     ```
     `:stale_before_ms = now_ms - WaitResumeLease`。分支对应关系（与 `runrt.go:905-958` 逐条一致）：
     同 owner 且 refresh → 任何阶段都可刷新；执行栅栏阶段且非「同 owner 刷新」→ 不动；
     他人持有未过期租约 → 不动；无 owner、租约无时间（旧记录）或已过期（含自己的过期租约）→ 认领。
     本语句已在 SQLite 上实测：p1 认领得到 `claimed`，p2 在 p1 租约未过期时更新 0 行。更新到 0 行时再用一条 `SELECT resume_owner, resume_phase`
     区分 found/owned，以保持原返回值 `(claimed, owned, found)` 的全部语义——**先把现有
     `claimWaitResume` 的每个分支写成表驱动测试并在旧实现上通过**，再替换实现，测试不改。
   - `ReleaseWaitResumeOwner`：`UPDATE … SET resume_owner='', resume_claimed_at_ms=NULL, resume_phase='' WHERE run_id=? AND action_id=? AND resume_owner=? AND resume_phase NOT IN ('execution_started','outcome_uncertain')`；0 行时按原语义区分「不是本 owner（返回 nil）」与「已越过执行栅栏（返回原错误）」。
   - `BeginWaitResumeExecution`：`… SET resume_phase='execution_started', resume_claimed_at_ms=? WHERE … AND resume_owner=? AND resume_phase NOT IN ('execution_started','outcome_uncertain')`。
   - `MarkWaitResumeUncertain`：`… SET resume_phase='outcome_uncertain' WHERE … AND resume_phase='execution_started'`（已是 uncertain 视为成功）。
   - `WaitResumeOwnedBy`：`SELECT resume_owner = ? FROM fb_run_waits WHERE …`。
   `GetWaitForRun`：按主键取，删 ORDER BY。`FindRunByAction`：按 `action_id`（UNIQUE）取。
   `waitRecoveryScan` 等读取改为选具体列。
6. **fb_actions.session_id**（S6/D14）：`CreatePending(ctx, sessionID, kind, payload)`、`CreateAsk(form)`
   要求非空 `form.SessionID`。调用方：
   - `pkg/run/runner.go:520-542`：会话取 `tool.ConversationSessionIDFromContext(ctx)`，为空则取
     `r.policyRunIDForTool(ctx)` 对应 run 的 `session_id`；两者都为空 → STOP（报告调用栈）。
     删掉把 `session_id` 塞进 payload 的那段（`:527-530`），`agent_id`/`subagent_type` 保留。
   - `pkg/tool/registry.go:367`：`form.SessionID` 已赋值，为空时同上从 run 取。
   - `pkg/gateway/api_extra.go:1862-1885`（HTTP `POST /actions/ask`）：请求体缺 `session_id` → 400，
     错误文本一句话；并校验该会话归属当前 agent。
   新增 `ActionService.ListForAgent(ctx, agentID string, f ActionFilter{Status, SessionID string}, limit int)`：
   ```sql
   SELECT a.<cols> FROM fb_actions a JOIN fb_sessions s ON s.id = a.session_id
   WHERE s.agent_id = ? [AND a.status = ?] [AND a.session_id = ?]
   ORDER BY a.updated_at DESC, a.id DESC LIMIT ?
   ```
   `handleActions`（`api_extra.go:1753-1826`）改为一次调用它，删掉 `queryLimit=-1`、逐行 `sessionOwned`、
   `actionConversationSession`（`server.go:183-202`）与 `turn.ActionSessionID`（`pkg/turn/approval.go:577-588`）
   ——所有用到后两者的地方改读 `Action.SessionID`。旧 `List` 若无其它调用方（testutil 改用新方法）则删除。
   前端：`grep -rn "session_id\|sessionId" frontend/src` 中读取 action payload 会话字段的地方改读列表行的
   `sessionId`（`actionListRow.SessionID` 已存在）。
7. **S8**：`ExpirePending` 改为一条
   `UPDATE fb_actions SET status='expired', error=?, updated_at=? WHERE status='pending' AND created_at < ? RETURNING id`；
   `ExpireIfPending` 若只剩它自己的调用方被删则按 deadcode 删除。
8. 测试：`ActivePrimaryRun` 租户隔离（另一 agent 的运行中 run 不返回）且不受 200 行限制；租约表驱动测试；
   `ListForAgent` 租户隔离；过期扫描一次返回全部过期 id。

**Verify**
- `grep -rn "source_json\|token_estimate\|ListRunsRecent\|ActionSessionID\|actionConversationSession\|\"default\"" pkg/state/runrt.go pkg/state/action_service.go pkg/gateway pkg/turn/approval.go` → 0 行（迁移代码除外）
- `grep -c "for attempt := 0; attempt < 8" pkg/state/runrt.go` → 0
- 相关包测试 + 全量测试通过；`cd frontend && pnpm build` 通过

**STOP**：租约表驱动测试无法在不改期望值的前提下通过新实现；某个 action 创建路径确实拿不到会话。

### T4 运行计时规范化（run 级计时迁到 `fb_runs`，消息行只留执行计时）

**目的**（owner 2026-09-27 裁决纳入）：
- 消除「run 级计时逐行重复」：同一 run 的所有 assistant 行计时完全相同（本机 292/292 个 run）。
- 消除「一列两义」：同名三列在 tool 行上存单个工具的执行计时（`pkg/state/message_sync.go:137-145`
  `toolMessageExecutionFields`），在 `!cmd` 用户行上存命令计时且没有 run（`pkg/tui/chat_session.go:2132`，本机 343 行）。
- 消除全库唯一的 TEXT 时间戳。
- 给旧行回填 `run_id`（本机 12935 条带计时的 assistant 行中 7873 条没有 `run_id`），从而删除 gateway 按时间窗口
  猜 run 的遗留代码（`pkg/gateway/api_extra.go:1056-1071` `chatMessageRunIDs`、`1109-1260` `assistantWindows` 一族），
  并让 TUI 回放给这些旧回合补回「Worked for」行（`replayWorkedLine` 要求 `RunID` 非空，`pkg/tui/commands.go:361-375`）。

本任务必须在 T5（事件合并）之前完成：遗留映射删除后，§4.4 不再需要迁移 `turn_started` 步骤。

**目标列**（§3.2 已是最终形态）：
- `fb_runs` 新增 `started_at_ms`、`finished_at_ms`、`worked_ms`：run 完成时由表面层（TUI/gateway，它们掌握
  「Worked for」的时钟）一次写入；三者同为 NULL（未完成/从未计时）或同为非 NULL（CHECK 约束）。
  `created_at`/`updated_at` 仍是行的生命周期时间，不是同一个事实，保留。
- `fb_messages` 删 `run_started_at`、`run_finished_at`、`worked_duration_ms`，新增 `exec_started_at_ms`、
  `exec_finished_at_ms`、`exec_duration_ms`：这一行**自身**工作的执行计时，只允许 tool 行与 `!cmd` 用户行有值
  （CHECK 约束）；三者都要保留——本机 tool 行 68/14917、`!cmd` 行 259/480 的时长与「结束-开始」不相等，不可互推。

**步骤**
1. schema.sql：`fb_runs` 的计时三列已由 T3 随最终 DDL 加入（T3 之后它们恒为 NULL）；本任务写入 `fb_messages` 的最终 DDL（§3.2）。在 §3.2 上用 sqlite3 验证：向 assistant 行写
   `exec_started_at_ms` 必须因 CHECK 失败。
2. 迁移，`migrateV1Messages` 内：tool 行与 `role='user'` 且 `run_started_at<>''` 的行：
   `exec_started_at_ms = CAST((julianday(NULLIF(run_started_at,'')) - 2440587.5) * 86400000 AS INTEGER)`，
   `exec_finished_at_ms` 同理，`exec_duration_ms = worked_duration_ms`；其余行三列为 NULL。
   任何非空旧值转换出 NULL → 迁移返回错误（旧值格式与预期不符，STOP）。
   （已实测：`2026-08-23T08:08:07.362187Z` 与 go-sqlite3 的 `2026-09-27 12:00:01.123456789+08:00` 两种格式都能正确转换。）
3. 迁移，新增 `migrateV1RunTiming`，在 `migrateV1Messages` 之后执行（Go 实现，读 `legacy_fb_messages`）：
   a. **已有 run_id 的回合**：对每个 `run_id<>''` 且存在 `role='assistant' AND run_started_at<>''` 行的 run，取其中
      `id` 最大的那行的三个值（转毫秒）写入 `fb_runs` 对应行。
   b. **旧回合**：对 `run_id='' AND role IN ('assistant','reasoning') AND run_started_at<>''` 的行，按
      `(session_id, run_started_at, run_finished_at, worked_duration_ms)` 分组（这三个值纳秒精度，一组即一次收尾写入的一个回合），
      每组得到 `min_id`、`max_id`、`s_sec`（开始秒）、`f_sec`（结束秒）。按 `min_id` 升序逐组处理：
      - 候选 run：同 `session_id`、`parent_run_id IS NULL`、`created_at BETWEEN s_sec-2 AND f_sec+1`、计时仍为 NULL
        且本次迁移尚未分配给其它组；按 `ABS(created_at - s_sec), created_at, id` 排序取第一个。
      - 没有候选时补建 run：`id = 'legacy-run-' || session_id || '-' || min_id`、`status='done'`、`input_text=''`、
        `parent_run_id=NULL`、`created_at=s_sec`、`updated_at=f_sec`、用量列 0。这不是编造数据——该回合确实发生过，
        只是当时没有 run 行记录它。
      - 写入该 run 的三个计时；`UPDATE fb_messages SET run_id=? WHERE session_id=? AND id BETWEEN min_id AND max_id AND run_id IS NULL`
        （区间内的 tool 行也属于这个回合；不回填它们会让 `replayWorkedLine` 在每个 tool 行前误判「run 结束」）。
      - 日志记录：组数、唯一匹配数、多候选数、补建数。本机实测预期：865 组、848 唯一、17 多候选、0 补建。
4. **写入端**：
   - 所有追加函数删除 `runStartedAt, runFinishedAt string, workedDurationMs int64` 三个参数：`AppendTurn`、
     `AppendToolTurn`、`AppendToolTurnWithMeta`、`AppendMessageWithSource*`、`AppendStructuredMessage`、
     `AppendMessageSequence`、`AppendNewMessages`、内部 `appendMessage`，以及 `pkg/turn/session.go` 的
     `UserTurnStore`/`AssistantTurnStore` 接口（`:218-221`、`:288-292`）与匿名接口断言（`:345-347`、`:375-377`）。
     只剩一个调用方或零调用方的包装按 deadcode 合并/删除。
   - tool 行的执行计时由 `appendMessage` 从 `msg.ToolExecutionTiming`（`*llm.ExecutionTiming`）直接写成三个毫秒整数；
     删除 `toolMessageExecutionFields` 的字符串格式化。
   - `!cmd`：`pkg/tui/chat_session.go:2126-2133` 改调新方法
     `AppendShellCommandTurn(ctx, sessionID, body, toolMetaJSON string, timing llm.ExecutionTiming) (int64, error)`
     （role 固定 `user`，写 exec 三列）；同处多余的 `Ensure` 调用（`:2127`）删除——追加路径自己会 upsert 会话。
   - **只有带 run 的收尾写入携带 run 计时**：新增 `type RunTiming struct { StartedAt, FinishedAt time.Time; Worked time.Duration }`；
     `AppendMessageSequenceForRun(ctx, sessionID, runID, msgs, model, usageJSON string, timing RunTiming)` 与
     `AppendStructuredMessageForRun(…, timing RunTiming)` 在**同一个事务**里追加行并执行
     `UPDATE fb_runs SET started_at_ms=?, finished_at_ms=?, worked_ms=?, updated_at=? WHERE id=?`（timing 为零值时不写）。
     `turn.AssistantTurn` 的 `StartedAt`/`FinishedAt` 改 `time.Time`，删 `FinishedAtUnix`（它只喂已删除的 `finished_at` 列）。
   - 调用方：`pkg/tui/chat_turn.go:497-515`（删 `formatTranscriptTime` 的使用，直接传 `completion.StartedAt/FinishedAt/Duration`）、
     `pkg/gateway/run_control.go:265-267/312-323` 的 opt 字段改 `time.Time`/`time.Duration`，其赋值处
     `pkg/gateway/server.go:2071-2073`、`api_extra.go:2328-2330` 去掉 `Format(time.RFC3339Nano)`。
     `pkg/turn/session.go:208-213` `transcriptTime` 与 TUI 的 `formatTranscriptTime` 若无其它调用方则删除。
5. **读取端**：
   - `storedMessage`/`Message` 删 `RunStartedAt/RunFinishedAt/WorkedDurationMs`，新增行级 `ExecStartedAtMs/ExecFinishedAtMs/ExecDurationMs`
     与 run 级 `RunStartedAtMs/RunFinishedAtMs/RunWorkedMs`（后三者由 `LEFT JOIN fb_runs r ON r.id = m.run_id` 读出，不存储在消息行）。
     T2 建立的 `messageColumns` 加 `m.` 前缀并追加 `r.started_at_ms, r.finished_at_ms, r.worked_ms`，所有消息查询 `FROM fb_messages m LEFT JOIN fb_runs r ON r.id = m.run_id`。
   - TUI：`replayWorkedLine` 用 `RunWorkedMs` 与 `time.UnixMilli(RunFinishedAtMs)`；`transcriptTurnTimingFromTurn`/
     `applyReplayToolTiming`（`commands.go:895-902`）用 `ExecDurationMs`。
   - gateway `/chat/sessions/:id/messages`（`api_extra.go:957-1040`）的 JSON 字段名与格式**不变**，在响应边界填充：
     tool 行与有 exec 值的 user 行 ← exec 三列；`role='assistant'` 且其 run 有计时的行 ← run 三列；其余行不填。
     毫秒在边界格式化为 RFC3339Nano（`time.UnixMilli(x).UTC().Format(time.RFC3339Nano)`）。前端不改。
   - 删除 gateway 遗留映射：`chatMessageRunIDs`、`assistantWindows`、`assistantWindowIndexForEvent`、`chatAssistantWindow`
     及其使用处（`api_extra.go:917-921` 读 run events、`:1000`、`:1024-1028` 的回退分支），`runID` 直接取 `t.RunID`。
6. **测试**：
   - 先写 gateway `/messages` 快照测试与 TUI 回放快照测试（新式行：带 run_id 的回合、tool 行、`!cmd` 行），在旧代码上通过；
     改完后**断言不改**仍通过。
   - 迁移测试：唯一候选、多候选、无候选（补建）三种旧回合；回填后区间内 tool 行带上 run_id；`!cmd` 行 exec 转换；
     旧 TEXT 格式不合法时迁移报错。
   - 旧回合 TUI 回放：每个回合恰好一行「Worked for」，文本与旧行上的耗时、结束时间一致。
   - CHECK：assistant 行写 exec 计时失败；run 三列不同时为空/非空失败。
7. **实机**（`run-forebrain`，临时 home，真实模型）：TUI 一轮带工具调用的对话 → 结束出现「Worked for …」；resume 后该行文本相同，
   工具卡时长相同；执行一次 `!cmd`，resume 后其卡片时长相同；web reload 后运行时间显示相同。在迁移后的演练库上打开一个旧会话：
   TUI 每个旧回合出现一行「Worked for」（新增的正确行为，写进报告），web 显示与迁移前一致。

**Verify**
- `grep -rn "RFC3339Nano" pkg/state pkg/turn/session.go pkg/tui/commands.go pkg/tui/chat_turn.go` → 0 行
- `grep -rn "chatMessageRunIDs\|assistantWindows\|toolMessageExecutionFields\|FinishedAtUnix\|WorkedDurationMs" pkg --include='*.go'` → 0 行
  （`worked_duration_ms` 作为 gateway JSON tag 保留，不在此 grep 内）
- `grep -rn "run_started_at\|run_finished_at" pkg --include='*.go' --include='*.sql'` → 只命中 gateway JSON tag、迁移代码与 `testdata/legacy_v0_schema.sql`
- 相关包测试、全量测试通过；快照测试断言未改

**STOP**：第 2 步有非空旧值转换为 NULL；快照测试在不改断言的前提下无法通过；某个 `PersistAssistantTurn` 调用方拿不到 RunID
（`pkg/tui/chat_turn.go:500`、`pkg/gateway/run_control.go:312` 两处目前都传了）。

### T5 事件日志合并（删除 `fb_run_steps`、`fb_tool_audit`）

**目的**：owner 裁决 1；修 D1–D5、D16、D18、S16–S18。合并后规则：**每个事实只在
`fb_session_events` 记一次**；按 run 读取的调用方改查同一张表。

**T5.0 先写保护网**（在改任何生产代码前，全部在旧代码上通过）：
- TUI 回放快照测试：选 `pkg/tui/commands_test.go`/`chat_surface_test.go` 里现有的回放用例为模板，
  构造一个同时包含主代理工具调用、主代理 plan 更新、子代理生命周期+工具+计划、goal、审批、MCP 启动失败
  的会话，断言 `replayTimelineWithReducer` 渲染出的帧文本；本任务结束时该断言**不改一字**仍通过。
- gateway `GET /chat/sessions/:id/messages` 与 `GET /chat/sessions/:id/events` 对同一构造会话的 JSON
  快照测试（去掉 sequence/时间等非确定字段后比较）。

**T5.1 schema 与迁移**：`fb_session_events` 最终 DDL；删 `fb_run_steps`、`fb_tool_audit` 两张表；
实现 `migrateV1SessionEvents`（§4.4 全部规则，含 Go 线性归并）；fixture 覆盖：主代理 TUI 工具步骤
（无对应事件→迁移）、gateway 工具步骤（已有同 step_id 事件→不迁）、`suppress_ui` 步骤、
`request_permissions`、主代理/子代理 plan、子代理生命周期步骤（不迁）、goal 步骤（不迁）、
turn_started 步骤（不迁：T4 删除遗留映射后它没有读取方）、会话不存在的旧事件（丢弃）。

**T5.2 写入端**——逐个处理全部 `AppendStep` 调用点。
`grep -rn "AppendStep(" pkg --include='*.go' | grep -v _test.go | grep -v "func (s \*RunStore) AppendStep"`
应恰好输出 18 行，与下表 #1–#17 覆盖的 18 个位置一一对应（#9 含 2 处，#11–15 含 5 处）；
数量或位置不符 → STOP。#18 不是调用点，只说明其余步骤类型的去向：

| # | 位置 | 处置 |
| --- | --- | --- |
| 1 | `pkg/tui/chat_turn.go:252` 步骤 hook | 删 `AppendStep`。主代理（`HookAgentIDFromContext==""`）的 `tool_call_started/completed` 与 `plan_updated`：`tool.RunEventFromStep(stepCtx, sid, rid, "tui", evt)` 返回 ok 时，调用 `RunStore.AppendSessionEventOnce` **只落库**（不走 `publishRunEvent`、不 notifyUI——这些卡片已由 hook 自己画）。`plan_updated` 落库后，原来由 run-step 镜像负责的 `notifyUI(PlanUpdatedMsg{…})` 移到这里直接调用（见 T5.3）。子代理/skill 分支原本就 `Publish`（它会落库），不变。 |
| 2 | `pkg/gateway/server.go:1739` 步骤 hook | 删 `AppendStep` 与紧随的 `AppendToolAudit`。主代理步骤已经经 `writeMsg` 的 `step` op 由 `wsevents.go:128` 镜像落库——先在代码里确认该 hook 的主代理分支确实调用了 `writeMsg(… Op: "step" …)`，否则 STOP。 |
| 3 | `pkg/gateway/approval.go:58` | 删 `AppendStep`（其后已 `Publish` 规范事件）。 |
| 4 | `pkg/gateway/approval.go:81` `appendAndPublishGatewayRunStep` | 改为直接 `event.NewRunEvent("", runID, sessionID, eventType, payload, now)` 后 `Publish`；函数改名 `publishGatewayRunEvent`。`run_control.go:242` 的调用随之更新。 |
| 5 | `pkg/tui/notify.go:948` `appendAndPublishTUIRunStep` | 同 #4，改名 `publishTUIRunEvent`，删除其中 `GetRun` 反查 session 的语句（调用方传入 sessionID）。 |
| 6 | `pkg/state/runrt.go:1006` `SetStatus` 的 `"error"` 步骤 | 删（存储层不产生事件，D18）。先逐个核对 `SetStatus(…, errMsg)` 的调用方（`grep -rn "\.SetStatus(" pkg`）：TUI 路径由 `notify.go` 发 `turn_error`、gateway 路径由 `run_error` op 镜像落库；某调用方没有对应的 `turn_error` 事件 → STOP 报告。 |
| 7 | `pkg/run/subagent.go:1163` 子 run 生命周期 | 删（`notifySubagentEndedDirect` 已发规范 `subagent_ended`）。 |
| 8 | `pkg/run/subagent.go:1259` `appendParentSubagentEvent` | 删除整个函数及其调用。 |
| 9 | `pkg/run/subagent.go:1304`、`:1886` | 删（`subagent_spawned` 已由 `notifySubagentSpawnedDirect` 发布）。 |
| 10 | `pkg/run/goal.go:301` `recordGoalEvent` | 删 `AppendStep`，只保留 `publishSurfaceEvent`；注释改为「写一次，run 级读取方按 run_id 读同一日志」。 |
| 11–15 | `pkg/gateway/server.go:1648/1940/1995/2040/2100`（turn_started / requires_action / turn_cancelled / turn_error / turn_completed） | 删。逐个确认同一代码块内有对应 `writeMsg` op（`run_started`/`requires_action`/`run_cancelled`/`run_error`/`run_completed`），它们由 `wsevents.go:70-99` 镜像落库；缺失 → STOP。 |
| 16 | `pkg/gateway/api_extra.go:2315` `answer` | 删（`:2320` 已发布 `assistant_delta`；`answer` 没有任何读取方——`MapRunEventType` 不映射它）。 |
| 17 | `pkg/gateway/run_control.go:190` `pending_input_updated` | 确认同处是否有 `writeMsg(Op: "pending_input_updated")`；有 → 删；没有 → 改为 `Publish` 规范事件。 |
| 18 | `pkg/run/orchestration_llm.go:1080` 的 `tool_slow` 等只经 hook 流入 #1/#2 的步骤 | 不单独处理：#1/#2 删除后自然不再持久化；`tool_slow`、`tool_parallel_*` 没有任何持久化读取方（`MapRunEventType` 不映射）。 |

同时删除：`RunStore.AppendStep`、`StepEvent`、`Subscribe`/`broadcast`/`ensureSubs` 及 `RunStore` 中的订阅字段
（`runrt.go:134-199`、`444-469`）、`ListStepsByRun`、`ListStepsByRunOfTypes`、`AppendToolAudit`、
`AppendToolAuditFunctionCall`、`ListToolAuditBySession`、`ListToolAuditByRun`、`ToolAuditRow`、整个
`pkg/state/audit.go`；`pkg/run/telemetry.go` 的 `runnerFCTelemetrySink`（它唯一的作用是写审计表）
及其注册处——先确认 `telemetry.FCInvocationRecord` 没有其它 sink，有则只删这一个。

**T5.3 读取端**

| 读取方 | 改为 |
| --- | --- |
| `turn.Service.ListRunEvents`（`pkg/turn/events.go:17-43`） | 新 `RunStore.ListRunEvents(ctx, runID, limit)`：`SELECT … FROM fb_session_events WHERE run_id=? ORDER BY sequence LIMIT ?`，直接转 `event.RunEvent`，**不再投影** |
| `turn.Service.ListSessionRunEvents`（`events.go:45-65`） | 删除。TUI `SurfaceSessionPlanUpdates`（`pkg/tui/chat_surface.go:747-780`）改用 `ListSessionEventsOfType(sid, "plan_updated")` 并保留 `AgentID==""` 过滤与按时间排序（gateway 消息接口对它的使用已在 T4 随遗留映射一起删除） |
| `goalOfRun`（`pkg/run/goal.go:311-340`） | 新 `RunStore.ListRunEventsOfTypes(ctx, runID, types...)`；`step.CreatedAt` 秒 → 事件 `CreatedAt` |
| TUI run-step 镜像 `startTUIRunEventMirror`/`notifyTUIRunStep`/`planUpdatedPayloadFromStep`（`pkg/tui/notify.go:687-760`） | 删除；主代理 plan 的 live 通知在 T5.2 #1 的 hook 里直接 `notifyUI(PlanUpdatedMsg{Payload: plan})`，**必须保持与原来相同的出现时机与顺序**（TUI 实机验证见 T5.6） |
| `pkg/testutil/scenario.go:73-80` | 改读 `ListRunEvents` |
| gateway `handleRunEvents` 的 `RunRT.ListStepsByRun` 兜底分支（`api_extra.go:1726-1745`） | 删除，只留 `Core.ListRunEvents` 分支 |
| `turn.MapRunEventType`、`ProjectRunEvent(s)`、`hiddenToolRunEvent`、`projectedRunEventPayload`、`subagent*PayloadFromStep`、`planUpdatedPayloadFromStep`、`tokenBudgetPayloadFromStep`、`ExtractTurnDiffPayload`（若只被投影使用） | 按 deadcode 删除；`gateway/wsevents.go` 的 `stepRunEventPayload` 若仍在用其中的映射，保留被用到的那部分 |

**T5.4 审计接口重写**（D1–D5）。语义取舍（交付报告里提请 owner 复核）：合并后审计数据就是
工具完成事件，因此只包含 `tool.RunEventFromStep` 认定为用户可见的调用——被 `suppress_ui` 隐藏的内部调用、
`request_permissions`、以计划卡呈现的 `session_todo` 不再出现在审计面板，与 TUI/web 的显示范围一致。
- `/chat/sessions/:id/tool-audit`：先校验会话归属（与 `handleChatSessionEvents` `api_extra.go:1083-1091` 相同写法），
  再一条 SQL：`WHERE session_id=? AND event_type='tool_call_completed' [AND run_id=?] [AND payload_json->>'$.tool_name'=?] ORDER BY sequence DESC LIMIT ?`（过滤在 SQL 里，D2）；`source` 参数删除。
  响应改为 `[{id: event_id, runId, sessionId, toolName, detailJson, createdAt}]`，`detailJson` = 事件 payload 原文，
  保持前端 `ToolAuditRow`（`frontend/src/lib/api.ts:661-668`）的字段名；`id` 由 number 改 string，
  前端 `acceptDiff(row.id)`（`ChatView.vue:302`）等用到 id 的地方同步类型。
- `/cost-summary`：校验归属；`SELECT payload_json->>'$.tool_name', COUNT(*) … GROUP BY 1`。
- `/rewind-last`：校验归属；取最近一条 `tool_name IN ('edit_file','write_file')` 的完成事件，路径取
  `payload_json->>'$.tool_meta.input.file_path'`（这两个工具的入参键是 `file_path`，见
  `pkg/tool/file_tools_test.go:98`）。写测试证明修复前返回 400、修复后返回 200。
- 前端 `extractDiffPreview`/`extractReferencedPaths`（`ChatView.vue`）按新 payload 结构（`tool_meta.input`、`output`）读取。

**T5.5 其它**：
- 删 `schema_version` 列：`SessionEvent.SchemaVersion` 字段删除；转线上 `event.RunEvent` 的地方
  （`wsevents.go:618-633`、`pkg/tui/notify.go:762-781`、`chat_surface.go:728`）填 `event.RunEventSchemaVersion`。
- S18：`AppendSessionEventOnce` 改 `INSERT … ON CONFLICT(session_id,event_id) DO NOTHING RETURNING sequence, run_id, event_type, payload_json, occurred_at_ms`；有返回行即新插入，否则才执行现有 SELECT。
- `run_id` 为空串的事件写 NULL；读出 NULL→""。
- **D16**：MCP 启动失败事件（`mcpStartupErrorEvent`，`pkg/tui/notify.go:966-995` 的发布方）在会话行尚不存在时
  只 notifyUI、不落库：这类失败发生在会话存在之前，不属于任何会话的历史；恢复会话时 MCP 会重新启动，
  当时的失败（若有）会作为新事件记入。实现为发布前 `HasSession(ctx, sid)`（结果为 false 时不落库）。
  写测试：会话不存在时发布不报错、库中无该事件；会话存在时事件落库。

**T5.6 实机验证**（`run-forebrain` 技能，临时 home，真实模型）：
1. TUI 新会话：让模型调用 `read_file`、`shell`、`session_todo`（产生 plan 卡）、派一个子代理；观察 live 画面；
   退出后 `resume` 该会话，回放画面与 live 一致（工具卡、计划卡位置、子代理卡、"Worked for" 行）。
2. 同一会话在 web 打开（gateway），reload，主对话与子代理视图与 TUI 一致；Tool audit 面板有记录、无重复；
   cost summary 计数 = 实际调用次数；对一次 `edit_file` 执行 rewind-last 成功。
3. gateway 新会话同样做 1、2，并在 TUI resume。
4. `sqlite3 "file:<临时 home 库>?immutable=1" "select name from sqlite_master where name in ('fb_run_steps','fb_tool_audit')"` → 空。

**Verify**
- `grep -rn "fb_run_steps\|fb_tool_audit\|AppendStep\|ToolAudit\|ListStepsByRun\|appendParentSubagentEvent\|ListSessionRunEvents\|startTUIRunEventMirror" pkg frontend/src` → 只命中迁移代码与 `testdata/legacy_v0_schema.sql`
- T5.0 的保护网测试原样通过；全量测试、`pnpm build` 通过

**STOP**：T5.2 表中任一「确认」不成立；T5.0 保护网在不改断言的前提下无法通过；实机回放与 live 不一致。

### T6 files

**目的**：`fb_files` 最终形态；修 D15；删死配置。

**步骤**
1. schema + `migrateV1Files`（§4.3）；fixture 覆盖 local、s3、`workspace_file` 行。
2. `File` 结构（`pkg/state/file_service.go:717-741`）改为与新列一一对应；`insert`、`Get`、
   `UpdateParsedTextLocal`（改名 `UpdateParsedText`）、`UpdateParseFailed` 改列；所有
   `IFNULL(source,'attachment')='attachment'` 条件删除；`LocalPath`/`EnsureLocalFile`/
   `EnsureParsedText`/下载接口改读 `StorageBackend`+`StorageBucket`+`StorageKey`。
3. 删 `FileStore.ProjectLookup`、`projectIDFor` 及 `pkg/gateway/serve_run.go:103` 的赋值；删
   `ParsedTextOSSConfig`、`Config.ParsedText`、`FOREBRAIN_PARSED_TEXT_OSS_*` 环境变量读取、
   `ParsedTextBackend` 类型与常量（它们从未影响行为）。`oss_version_id/endpoint/etag` 相关赋值删除。
4. **D15**：`Get(ctx, agentID, id)` 用 `JOIN fb_sessions s ON s.id=f.session_id AND s.agent_id=?`；
   `handleFileOne`、`handleFileDownload`、`/files/:id/text`（`api_extra.go:731` 附近）与
   `pkg/gateway/run_control.go:408` 的 `EnsureLocalFile` 都传当前 agent；另一 agent 的文件返回 404。
5. 测试：跨租户 404；s3 行迁移后下载路径取 `storage_key`。

**Verify**：`grep -rn "oss_\|OSSKey\|OSSBucket\|OSSEtag\|parsed_text_backend\|ParsedTextOSS\|workspace_file\|ProjectLookup" pkg --include='*.go' --include='*.sql'`
→ 只命中 `OSSConfig`（对象存储连接配置本身保留）、迁移代码与 testdata；测试通过。

### T7 cron 与 heartbeat

**目的**：三表最终形态；修 R4–R6、D9–D12、D19、S23。

**步骤**
1. schema + `migrateV1CronJobs`、`migrateV1CronRuns`、`migrateV1Heartbeats`（§4.3）。
2. `CronJob.LastRunAt`、`CronRun.FinishedAt`、`Heartbeat.LastFiredAt`/`NextRunAt` 改指针或
   `sql.NullInt64`（沿用 `CronJob.NextRunAt *int64` 的现有写法）；删 `CronRun.RunID`、
   `Heartbeat.AgentID`、`Heartbeat.Paused` 字段（API JSON 的 `paused` 由 `next_run_at == nil` 计算后输出，
   保持前端字段不变——在 gateway 的响应构造处计算，不在存储层）。
3. 拆 `SaveJob`：`InsertJob`（创建）与 `UpdateJobConfig`（只写 `project_id,name,schedule,prompt,deliver,enabled,repeat_limit,next_run_at,updated_at`，`WHERE id=? AND agent_id=?`）；
   `pkg/process/cron_service.go` 的 `CreateJob`/`UpdateJob`/暂停恢复改用它们。
4. **R5/D10 开火认领**：`runDueJobs`（`scheduler.go:106-124`）对每个到期任务先算 `next`（`nextFire` 需要
   `run_count+1` 后的值）再执行
   `UPDATE fb_cron_jobs SET next_run_at=?, run_count=run_count+1, last_status='running', updated_at=? WHERE id=? AND agent_id=? AND next_run_at=?`（最后一个参数是 `DueJobs` 读到的到期值），
   `RowsAffected==1` 才开火；进程内 `s.claim` 保留为「同进程不叠跑」的语义（它不是跨进程手段）。
   `RunNow`（手动）不推进调度。
5. **R4/D9 结果写回**：`recordOutcome` 改为
   `UPDATE fb_cron_jobs SET last_run_at=?, last_status=?, last_error=?, last_output=?, failure_streak=CASE WHEN ?='failed' THEN failure_streak+1 WHEN ?='ok' THEN 0 ELSE failure_streak END, updated_at=? WHERE id=?`；
   不再写 `run_count`/`next_run_at`/配置列；任务已删则更新 0 行，什么也不做。
6. **heartbeat**：`DueHeartbeats(ctx, agentID, at)` 加 `JOIN fb_sessions s ON s.id=h.session_id AND s.agent_id=?`（D12），
   `WHERE h.next_run_at IS NOT NULL AND h.next_run_at <= ?`；开火前 CAS 推进
   `UPDATE fb_heartbeats SET next_run_at=? WHERE session_id=? AND next_run_at=?`（R5）；`reanchor` 改为
   `UPDATE fb_heartbeats SET next_run_at=?, last_fired_at=COALESCE(?, last_fired_at), updated_at=? WHERE session_id=? AND next_run_at IS NOT NULL`（R6）。
   `ApplyHeartbeatInterval` 暂停时 `NextRunAt=nil`。
7. **S23/D19**：`DueJobs` 删 `enabled = 1` 谓词；schema 注释改为准确描述（`enabled` 表达用户意图，
   `next_run_at IS NULL` 表示暂停或已用尽，到期查询只看后者）。
8. 测试：运行期间暂停→结果写回后仍暂停；运行期间删除→写回后不存在；两个 Scheduler 实例（模拟两进程，
   共享同一 `*sql.DB` 但各自 `claim` 表）对同一到期任务只开火一次；清除的心跳在 reanchor 后不复活；
   另一 agent 的心跳不被当前调度器开火。

**Verify**：`grep -n "SaveJob\b\|SaveHeartbeat\|paused" pkg/state/cron.go pkg/turn/scheduler.go` → 无全行 upsert 残留（`SaveHeartbeat` 若保留只用于创建/用户设置）；测试通过。

### T8 记忆表

**步骤**
1. schema + `migrateV1MemoryStage1Outputs`、`migrateV1MemoryJobs`（§4.3）；`fb_memory_index_files` 与
   FTS 在迁移中清空重建（§4.2）。
2. 删 `worker_id` 的写入（`jobs.go:213-222`、`489`、`511`）；`usage_count` 相关 `COALESCE(usage_count,0)` 删除，
   插入时写 0（`store.go:164-176`）。
3. **S7**：`ftsindex.go` 刷新一个文件时，在同一事务内：`DELETE FROM fb_memory_fts WHERE rowid >= ? AND rowid < ?`
   （取自该文件 `fb_memory_index_files` 的 `first_rowid`/`line_count`）→ `first = (SELECT IFNULL(MAX(rowid),0)+1 FROM fb_memory_fts)`
   → 用显式 rowid `first+i` 插入各行 → upsert 文件记录（含 `first_rowid`、`line_count`）。删除文件同理。
   （该事务由 `_txlock=immediate` 保证串行，rowid 连续。）
4. 测试：刷新一个文件只删除它自己的行；基准（附录 A 的 FTS 项）从 ~1.3s 降到毫秒级。

**Verify**：`grep -n "WHERE root = ? AND path = ?" pkg/memory/ftsindex.go` → 不再用于 `fb_memory_fts` 的删除；测试通过。

### T9 顺带：会话级 IFNULL/死代码清扫

对 T1–T8 触及的全部 SQL，确认已无对 NOT NULL 列的 `IFNULL`/`COALESCE`；跑 `deadcode`，删除本计划造成的
全部无调用方代码（按 §1.2 的范围规则）。**Verify**：
`grep -rn "IFNULL(\|COALESCE(" pkg/state pkg/memory --include='*.go' | grep -v _test.go` 的每一行都作用于可空列（在报告中逐行列出其可空理由）。

### T10 history.db

**步骤**
1. `pkg/tool/loaded_skills.go`：DSN 与状态库一致（`_busy_timeout=5000&_journal_mode=WAL&_synchronous=NORMAL&_txlock=immediate`），
   驱动名沿用 `sqlite3`；删除 `migrateStore`、`ensureColumn`，换成与 §4.1 同构的 `user_version` 迁移
   （库小，不需要 VACUUM 与长 busy_timeout）：v0（表 `commands`）→ v1（表 `output_records`，§3.4）：
   `command=cmd`、`created_at_ms=CAST((julianday(timestamp)-2440587.5)*86400000 AS INTEGER)`（任一非空旧值转出 NULL
   → 报错）、`kind=IFNULL(kind,'shell')`，其余同名列拷贝；删旧表与旧索引。
2. `Save`/`Get`/`RecordRetrieve`/统计查询改新表名与列名；`Entry.Timestamp` 读写改毫秒。
3. 测试：旧库 fixture（用当前 DDL 建 + 三行，含一行缺 `kind` 列的更老形态）迁移后结构等于新建库；统计结果不变。

**Verify**：`grep -n "commands\b\|ensureColumn\|timestamp TEXT" pkg/tool/loaded_skills.go` → 0 行；测试通过。

### T11 收尾与真实库演练

1. `deadcode`、`go vet`（含 linux/windows）、包依赖图、全量测试、`pnpm build` 全部通过。
2. 重跑附录 A 基准，与 `/tmp/fb_baseline_bench.txt` 并列写进交付报告；S1/S3/S4/S6/S7 必须降到 10ms 以内。
3. 真实库演练：`cp /tmp/fb_rehearsal/pristine.sqlite /tmp/fb_rehearsal/work.sqlite`，用新二进制以临时 home 打开
   （把 work.sqlite 放到临时 home 的 `state/forebrain.state.sqlite`）：
   - 迁移日志打印各表迁移/丢弃行数，丢弃行数与 §4.3 的「丢弃」列可解释的数量一致（本机预期：
     5 条孤儿事件、`fb_tool_audit` 全部、`fb_run_steps` 中不迁的部分、`fb_jobs`）；
   - §4.5 第 1、2 条在真实库上成立；
   - 附录 B：用新代码对迁移后的库导出 `context_after.txt`，与 T0 的 `context_before.txt` **逐行相等**（缓存不变）；
   - history.db 同样演练。
4. 实机：在迁移后的临时 home 上 resume 迁移前最近的 3 个会话（TUI 与 web 各一次），画面正常、无重复卡片、
   无孤儿审批。
5. 删除附录 B 的临时导出测试文件。
6. 把交付报告（改动文件清单、每个缺陷 D1–D20 的修复位置与测试名、基准对比、演练数据、deadcode 中与本计划
   无关的既有条目）写在对话里交给 owner；**不 commit**。

## 8. 总完成标准（全部满足）

- [ ] `CGO_ENABLED=1 go test -tags fts5 ./... -count=1` exit 0
- [ ] `go vet ./...`、`GOOS=linux go vet ./...`、`GOOS=windows go vet ./...` exit 0
- [ ] `cd frontend && pnpm build` exit 0
- [ ] `pkg/state/schema.sql` 与 §3.2 结构一致；`TestStateMigrationMatchesFreshSchema` 通过
- [ ] 迁移后 `PRAGMA foreign_key_check` 为空；`PRAGMA user_version` = 1
- [ ] `grep -rn "fb_run_steps\|fb_tool_audit\|fb_work_items\|fb_todos\|fb_intel_audit\|addColumnsMissingFrom\|ensureColumn\|normalizeSessions" pkg frontend/src` 只命中迁移代码与 `testdata/legacy_v0_schema.sql`
- [ ] 真实库演练 `context_before.txt` 与 `context_after.txt` 完全相同
- [ ] 基准：S1、S3、S4、S6、S7 ≤ 10ms（附录 A 数据量下）
- [ ] D1–D20 每项都有对应测试，且测试在修复前的代码上失败（报告中给出证据）
- [ ] TUI 与 web 实机验证（T4 第 7 步、T5.6、T11.4）截图/读屏记录在报告中
- [ ] `git log` 无新提交；`git status` 只含范围内文件

## 9. STOP 条件（全局）

出现以下任一情况，停下来向 owner 报告，不要自行变通：

- §1.3 漂移校验不通过。
- 任何改动会改变发往模型的字节（缓存规则）——包括迁移后 `context_before/after` 不一致。
- 某个「只写不读」列在删除时编译报错于 `pkg/state` 之外（说明存在真实读取方）。
- 任务里写明「确认…否则 STOP」的前提不成立（T3.3、T3.6、T4 第 2 步、T5.2 各行、T5.6、T10.1）。
- 迁移在真实库副本上 `foreign_key_check` 非空且违例无法归入 §4.3 的丢弃规则。
- T0 发现既有测试失败（先报清单，由 owner 决定并入方式）。
- 同一 Verify 修两次仍不通过。

## 10. 维护说明

- **以后任何表结构变更**：改 `schema.sql` → `stateSchemaVersion+1` → 在 `stateSchemaMigrations` 追加一个迁移
  （从上一版本到新版本，含旧表旧字段的清理）→ 更新 `legacy_v0_fixture` 之外新增该版本的 fixture →
  `TestStateMigrationMatchesFreshSchema` 必须对每个历史版本都成立。不要再写任何「开库时补列」的代码。
- `testdata/legacy_v0_schema.sql` 是历史快照，永远不要修改。
- `fb_session_events` 现在同时是会话游标日志和 run 账本：新增事件类型时，想清楚它是否被表面回放
  （TUI `replayTimelineWithReducer`、web `applyObservedEvent`）——主代理的、由 transcript 行重建的类型要让
  两端回放都跳过（参考 `replayToolEventSkipped` 与前端 `transcriptBackedEventTypes`）。
- 评审重点：迁移的丢弃规则（数据是否真的不可达）；租约 SQL 与原分支语义的一一对应；
  TUI plan 卡 live 通知时机；审计接口的租户校验。
- 本计划明确不做（§6 范围外）：子代理流式片段的持久化压缩（owner 裁决维持现状）。

---

## 附录 A：性能基准脚本

在仓库根目录执行（只在 `/tmp` 写文件）：

```python
# save as /tmp/fb_bench.py ; run: python3 /tmp/fb_bench.py
import sqlite3, time, os
path = '/tmp/fb_bench.db'
if os.path.exists(path): os.remove(path)
db = sqlite3.connect(path)
db.executescript(open('pkg/state/testdata/legacy_v0_schema.sql' if os.path.exists('pkg/state/testdata/legacy_v0_schema.sql') else 'pkg/state/schema.sql').read())
# NOTE: after T1 the live schema.sql differs; to measure the NEW queries, build the DB by
# running the new binary's Open on it (or execute the new schema.sql) and adapt column names.
N_S, M_PER, N_RUNS, N_ACT = 2000, 500, 60000, 200000
now = 1790000000
db.execute('BEGIN')
db.executemany("INSERT INTO fb_sessions(id,agent_id,title,updated_at,created_at) VALUES(?,?,?,?,?)",
               [(f"s{i}", 'main', f"s{i}", now + i, now) for i in range(N_S)])
db.executemany("INSERT INTO fb_messages(session_id,role,content,created_at,updated_at,source) VALUES(?,?,?,?,?,?)",
               [(f"s{i}", 'assistant' if j % 2 else 'user', 'x' * 50, now + j, now + j, 'transcript')
                for i in range(N_S) for j in range(M_PER)])
db.executemany("INSERT INTO fb_runs(id,session_id,input_text,status,parent_run_id,created_at,updated_at) VALUES(?,?,?,?,?,?,?)",
               [(f"r{k}", f"s{k % N_S}", 'in', 'done', '' if k % 3 == 0 else f"r{k - (k % 3)}", now, now + k) for k in range(N_RUNS)])
db.executemany("INSERT INTO fb_actions(id,kind,status,payload_json,created_at,updated_at) VALUES(?,?,?,?,?,?)",
               [(f"a{k}", 'tool_approval', 'approved' if k % 50 else 'pending', '{"session_id":"s%d"}' % (k % N_S), now + k, now + k) for k in range(N_ACT)])
db.executemany("INSERT INTO fb_memory_fts(terms,root,path,line_no) VALUES(?,?,?,?)",
               [(f"w{l} term{f}", '/root', f"f{f}.md", l) for f in range(2000) for l in range(250)])
db.commit()
def T(label, sql, args=(), n=5):
    t = time.time()
    for _ in range(n): db.execute(sql, args).fetchall()
    print(f"{label}: {(time.time() - t) / n * 1000:.1f} ms")
T("S1 SessionIDWithLatestMessage", "SELECT m.session_id FROM fb_messages m JOIN fb_sessions s ON s.id = m.session_id AND s.agent_id = 'main' WHERE IFNULL(TRIM(m.session_id), '') != '' AND IFNULL(m.source, 'transcript') = 'transcript' GROUP BY m.session_id ORDER BY MAX(m.created_at) DESC LIMIT 1", n=2)
T("S3 run_tree usage", "WITH RECURSIVE run_tree(id) AS (SELECT id FROM fb_runs WHERE session_id=? UNION SELECT r.id FROM fb_runs r JOIN run_tree t ON r.parent_run_id=t.id) SELECT IFNULL(SUM(usage_prompt_tokens),0) FROM fb_runs WHERE id IN (SELECT id FROM run_tree)", ("s5",))
T("S4 ListChildRuns", "SELECT id FROM fb_runs WHERE parent_run_id=?", ("r300",))
T("S5 ListRunsRecent", "SELECT id FROM fb_runs ORDER BY updated_at DESC LIMIT 200")
T("S6 Actions.List(all,-1)", "SELECT id,kind,status,payload_json FROM fb_actions ORDER BY updated_at DESC, id DESC", n=2)
T("S7 FTS delete-scan one file", "SELECT count(*) FROM fb_memory_fts WHERE root = '/root' AND path = 'f7.md'")
t = time.time()
db.execute("UPDATE fb_sessions SET updated_at = MAX(COALESCE(updated_at, 0), COALESCE((SELECT MAX(COALESCE(NULLIF(m.updated_at, 0), m.created_at)) FROM fb_messages m WHERE m.session_id = fb_sessions.id), 0), COALESCE(created_at, 0))")
db.execute("UPDATE fb_sessions SET message_count = (SELECT COUNT(*) FROM fb_messages m WHERE m.session_id = fb_sessions.id AND IFNULL(m.source, 'transcript') = 'tui')")
db.rollback(); print(f"S2 normalizeSessions (2 of 4 stmts): {(time.time() - t) * 1000:.1f} ms")
```

2026-09-27 基线（Apple Silicon，本机）：S1 3330.9 / S3 300.4 / S4 26.6 / S5 131.8 / S6 1693.3 / S7 1312.7 / S2 1940.7 ms；
加 `parent_run_id` 索引后 S3 1.1、S4 0.0 ms；S1 的新写法 0.0 ms。T11 复测时用新 schema 建库、新查询语句。

## 附录 B：缓存字节基线导出（临时测试，T11 结束后删除）

新建 `pkg/state/zz_context_dump_test.go`（T0 在**旧代码**上建并运行一次，T11 在新代码上再运行一次，然后删除）：

```go
package state

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"testing"
)

// Run: FOREBRAIN_CONTEXT_DUMP_DB=/tmp/fb_rehearsal/<db> FOREBRAIN_CONTEXT_DUMP_OUT=/tmp/fb_rehearsal/context_<phase>.txt \
//   CGO_ENABLED=1 go test -tags fts5 ./pkg/state -run TestZZContextDump -count=1
func TestZZContextDump(t *testing.T) {
	path, out := os.Getenv("FOREBRAIN_CONTEXT_DUMP_DB"), os.Getenv("FOREBRAIN_CONTEXT_DUMP_OUT")
	if path == "" || out == "" {
		t.Skip("dump env not set")
	}
	ctx := context.Background()
	db, err := Open(ctx, path, nil) // T0: old Open on a COPY; T11: new Open (migrates the copy)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	rows, err := db.QueryContext(ctx, `SELECT id, agent_id FROM fb_sessions`)
	if err != nil {
		t.Fatal(err)
	}
	type sess struct{ id, agent string }
	var all []sess
	for rows.Next() {
		var s sess
		if err := rows.Scan(&s.id, &s.agent); err != nil {
			t.Fatal(err)
		}
		all = append(all, s)
	}
	rows.Close()
	sort.Slice(all, func(i, j int) bool { return all[i].id < all[j].id })
	f, err := os.Create(out)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	for _, s := range all {
		store := NewSessionStore(db, s.agent)
		msgs, err := store.ListTranscriptMessages(ctx, s.id, 5000)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := json.Marshal(msgs)
		var ps []string
		prow, _ := db.QueryContext(ctx, `SELECT key, value FROM fb_session_prompt_state WHERE session_id=? ORDER BY key`, s.id)
		for prow.Next() {
			var k, v string
			_ = prow.Scan(&k, &v)
			ps = append(ps, k+"\x00"+v)
		}
		prow.Close()
		pb, _ := json.Marshal(ps)
		fmt.Fprintf(f, "%s %x %x\n", s.id, sha256.Sum256(b), sha256.Sum256(pb))
	}
}
```

T0 时对 `pristine.sqlite` 的一个副本运行（旧代码的 `Open` 会对副本做旧式补列与归一化，不影响 transcript 字节）；
T11 时对演练用的 `work.sqlite` 运行。两次输出必须逐行相同。

---

## 11. Implementation Status

> 只记录执行事实与偏差，不改动本文任何要求。逐任务追加；每格「Verify」指该任务小节里的 Verify。

| 任务 | 状态 | 证据 |
| --- | --- | --- |
| T0 基线 | 完成 | `/tmp/fb_baseline_tests.txt`、`/tmp/fb_baseline_bench.txt`、`/tmp/fb_rehearsal/`、`context_before.txt`（283 行 = 会话数） |
| T1 迁移框架/DSN/删死表 | 完成 | `pkg/state/schema_migrations.go` + `db.go`；`TestStateMigrationMatchesFreshSchema`、`TestStateOpenIsIdempotentAtCurrentVersion`、`TestOpenRejectsStateDatabaseFromANewerBinary`、`TestReadOnlyOpenRejectsOutdatedSchema` |
| T2 会话/消息/项目/UI 状态 | 完成 | 5 表最终形态 + 专用拷贝器 + `TestStateMigrationPreservesModelContextBytes`、`TestConcurrentSequenceAppendsDoNotDuplicateRows`、`TestConcurrentFirstMessagesNameTheSessionOnce` |
| T3 runs/waits/actions | 完成 | 单条条件 UPDATE 的 5 个租约函数、`ActivePrimaryRun`、`session_id` 列；fixture zeta 组 |
| T4 运行计时规范化 | 完成 | 计时回 `fb_runs`、消息行只留 `exec_*`；`TestChatMessagesTimingFieldsStable`、`TestReplayWorkedLinePerRun` |
| T5 事件日志合并 | 完成 | 见下；T5.6 隔离 home 实测（toolcall/tool 两模式 live+resume、gateway 三端点）通过，遗留两处既有 live/replay 差异见「待 owner 决策」 |
| T6 files | 完成 | 14 列最终形态 + `migrateV1Files`；`TestFileEndpointsHideAnotherAgentsFile`；Verify grep 只命中迁移代码与 testdata |
| T7 cron 与 heartbeat | 完成 | `InsertJob`/`UpdateJobConfig`/`ClaimDueJob`/`RecordOutcome`/`ClaimHeartbeat`/`ReanchorHeartbeat`；6 个新测试（暂停保留/删除不复活/双 Scheduler 单火/CAS 单赢/清除不复活/他租户心跳不开火） |
| T8 记忆三表 | 完成 | 复合 FK、`usage_count NOT NULL`、FTS rowid 范围删除（`TestReindexingOneFileDropsOnlyItsOwnRows`）；worker_id 链路删除 |
| T9 IFNULL/死代码清扫 | 完成 | `runrt.go`/`session_store.go` 剩余 5 处 NOT NULL 列 IFNULL 删除；deadcode 37 条全部既有（见交付报告） |
| T10 history.db | 完成 | `output_records` + `user_version` 迁移 + WAL/`_txlock=immediate` DSN；`TestStoreMigrationMatchesFreshSchemaAndKeepsStats` |
| T11 收尾与真实库演练 | 完成（S6 已由 owner 裁决接受现状，见「待 owner 决策」） | 全量 28 包绿、3 平台 vet 绿、`pnpm build` 绿、`deadcode` 37 条不变；真实库演练全项通过（见 T11 执行记录） |

### T5 执行记录

- **写出端**：全部 18 个 `AppendStep` 调用点按 §T5.2 表处置完毕；`turn.ProjectRunEvent(s)`/
  `MapRunEventType`/`ListSessionRunEvents`/`hiddenToolRunEvent` 等投影层整体删除，
  `turn.ListRunEvents` 直接读同一张事件表（新增 `turn.RunEventFromRecord`）。
- **读入端**：TUI `SurfaceSessionPlanUpdates` 改读 `ListSessionEventsOfType(plan_updated)`；
  `handleRunEvents` 只留 `Core.ListRunEvents`；`requiresLintFollowup` 读 `tool_call_completed` 事件。
- **审计三端点**：新增 `state.ListSessionToolCalls`/`CountSessionToolCalls`/`LatestRewindableToolCall`
  （过滤在 SQL 内，含会话归属校验），`source` 参数删除，`id` 由 number 改 string（前端同步）。
- **D16**：MCP 启动失败在会话行不存在时只渲染不落库（`recordMCPStartupFailures` 先 `HasSession`，
  失败走抽出的 `notifyTurnError`）；新增双半测试。
- **顺带修掉的缺陷**：迁移 SQL 里 `p` 不是合法表达式（改为把旧 `payload_json` 投影成 `p`）；
  `suppress_ui` 是 JSON 布尔（`->>` 得到 `"1"`）导致迁移过滤器失效；`SessionEventHighWater` 在
  无事件的会话上 `MAX(sequence)` 为 NULL 而报错。

#### 与计划的偏差（需 owner 知晓）

1. **文件命名规则**（沿用 T1–T4 的偏差）：新增/改名的测试与生产文件遵守「≤2 下划线、测试须有同名
   生产文件」，因此计划里的 `zz_*`/临时测试文件并入既有文件。
2. **`AppendApprovalAudit`/`ApprovalAuditStore`/`ApprovalAudit` 删除**：审计表删除后它没有落点，
   而审批决定本身已由规范 `approval_resolved` 事件记录（live 与 replay 都读它）。同理删除 TUI 的
   `user_shell_command` 审计行。
3. **TUI 侧 `publishTUIRunEvent(ctx, sessionID, runID, …)`**：会话 id 由调用方传入；两个恢复路径
   （`interruptUncertainTUIApproval`、`failRecoveredApproval`）与 `abortPendingApproval` 各自拿到
   调用方已知的会话 id，不再在发布函数里反查 run。
4. **`pkg/run/telemetry.go` 的 FC-invocation sink 整条链路删除**：它唯一的生产调用方是
   `runner.Load`，删除后 `SetFCInvocationSink`/`NewFCInvocationMiddleware` 成为无调用方代码，
   按 §1.2 一并删除（`pkg/telemetry/fc_invocation.go` 与 `pkg/tool/registry.go` 的注册处）。
   这是本次唯一触碰 §6 文件清单之外的改动。
5. **§T5 Verify 的 grep 口径**：该 grep 无法只命中迁移代码与 testdata —— T5.4 明确要求保留
   `/chat/sessions/:id/tool-audit` 端点与前端 `ToolAuditRow` 类型，二者本身含 `ToolAudit` 子串；
   fixture 也必须 `INSERT INTO fb_run_steps`。实际命中＝迁移代码 + 两个 testdata 文件 +
   一个端点路由/处理函数名 + 前端类型，外加 `pkg/gateway/dist`（构建产物）。
6. **`deadcode`**：本次改动不再产生新的无调用方函数（37 条与 T2 基线同数，全部既有）。

#### 待办

- T5.6 实机验证（临时 home + 真实模型：TUI 新建/回放一致、web 一致、tool audit 无重复、
  cost summary 计数、rewind-last 成功、`fb_run_steps`/`fb_tool_audit` 不存在）。

### T6–T11 执行记录

- **T6**：`fb_files` 14 列 + FK；`migrateV1Files`（storage_key = CASE backend、丢 workspace_file 与孤儿行）；
  D15 修复（`Get(ctx, agentID, id)` JOIN fb_sessions，五个 gateway 端点与 `prepareWebTurnInput`/
  `fileReferenceResolver` 都带租户）；删 `ProjectLookup`/`ParsedTextOSSConfig`/`FOREBRAIN_PARSED_TEXT_OSS_*`/
  `ParsedTextBackend`。连带清理：`CreateFromReader` 与表单/前端的 `run_id` 参数随列删除。
- **T7**：三表最终形态（见 §3.2）；`DueJobs` 删 `enabled=1`、due 部分索引；`DueHeartbeats` 经
  fb_sessions 过滤租户；开火前 CAS（`ClaimDueJob`/`ClaimHeartbeat`）+ 结果只写结果列
  （`RecordOutcome`）；`SaveHeartbeat` 仅剩用户设置路径；API 的 `paused` 由 gateway 响应处计算。
- **T8**：`fb_memory_stage1_outputs` 复合 FK + `usage_count NOT NULL DEFAULT 0`；`fb_memory_jobs` 删
  worker_id（连带三个 claim 函数与 `runStage2` 的 workerID 参数）；`fb_memory_index_files` 加
  first_rowid/line_count，FTS 删除改 rowid 范围（S7：52.1ms → 0.8ms）。
- **T9**：删 `runrt.go` usage IFNULL×2、`session_store.go` message_id/parts/content IFNULL×3（皆为
  NOT NULL 列）；其余 IFNULL/COALESCE 均作用于可空列（清单见交付报告）。
- **T10**：`OpenStore` DSN 与状态库一致；`migrateHistoryStore` 按 `user_version` 重建 v0 `commands`
  → `output_records`（容忍缺列的更老形态、毫秒 ROUND 转换、v0 索引删除）；两个迁移测试。
- **T11 演练**（`/tmp/fb_rehearsal/work.sqlite`，795MB pristine 副本）：
  - 迁移 ~25s（含 VACUUM，795MB → 604MB）；`user_version=1`；`PRAGMA foreign_key_check` 0 行；
    sqlite_master 与新库逐条一致；`fb_run_steps`/`fb_tool_audit`/`fb_jobs`/`legacy_*` 全部消失。
  - 行数：sessions=283、messages=70035（=原库）、events=92863（62148 事件 + 30715 迁移步骤）、
    runs=1223（482 + 741 个 T4 补建 legacy-run）、actions=807（原 811，4 条孤儿丢弃）、
    stage1=85、memory_jobs=155。丢弃可解释：tool_audit 34061（整表）、孤儿事件 5、steps 按规则 4612。
  - `context_after.txt` 与 T0 `context_before.txt` **283 行逐行相等**（缓存字节不变）。
  - history.db 副本演练：201 行全保留、stats 不变、v0 表与索引消失、sqlite_master 与新库一致。
  - TUI resume 最近 3 个会话：画面正常、无重复卡片、无孤儿审批（唯一 pending 是 2026-09-07 既有数据）；
    三次 resume 后 DB 无重复 message_id。web（gateway on 迁移库）：sessions/messages/events/context/
    tool-audit/cost-summary/todos 全 200，tool_calls=16 无双计。
  - 基准（新 schema 建库、新查询）：S1 0.1ms / S3 0.1ms / S4 0.0ms / S5(ActivePrimaryRun) 0.0ms /
    S7 0.8ms / S6-pending 4.0ms、S6-session 0.1ms、S6-agent-wide 50.7ms（对照基线 136.5ms；见下）。

#### 待 owner 决策（T11）

1. ~~**S6 的 ≤10ms 目标与 §3.2 索引集冲突**~~ **owner 裁决（2026-09-28）：接受现状**。§3.2 索引集不变；
   S6 的验收目标以页面实际走的热路径为准（web 只调用「pending + 指定会话」，0.1ms；pending 全租户 4.0ms，
   均 ≤10ms），整租户不限状态的形态记为已知最坏值 50.7ms（附录 A 数据量），不为它增加索引或冗余列。
   原始记录：`handleActions` 的 agent 全量形态（LIMIT 500）在附录 A
   数据量（20 万 action 全归一个 agent 的最坏情形）下 50.7ms——剩余成本是租户全量 top-K 排序，
   §3.2 的索引清单没有（也不允许有）租户级 `updated_at` 索引。生产热路径（status=pending 4.0ms、
   会话内 0.1ms）已达标；真实库 811 条 action 两种形态都是亚毫秒。选项：接受现状，或修订 §3.2
   增补 `fb_actions(updated_at DESC, id DESC)` 索引（写入放大的代价）。
2. ~~**T5.6 遗留的两处 live/replay 差异**~~ **已按 owner 指令修复（2026-09-28，见「T5.6 差异修复」）**。

### T5.6 差异修复（2026-09-28，owner 指令「定位根因并彻底修复」）

三处根因、三处修复，TUI 真机 A/B（run-forebrain toolcall/tool 两场景）live 与 resume 归一化后
逐行一致（唯一差异为预期 resume 提示行）：

1. **mode 卡 `··· N more lines` replay 缺失** —— 根因：live 通知为 `exit_plan_mode` 从工具输出解析
   `plan_file` 存入 `Message.FilePath`，全卡据此多渲染 `Plan file:` 行（长路径折 2 行 → 5 行 → 折叠
   hint）；replay 的 `replayToolMessage` 不设 FilePath，同卡只 3 行。修复：新增
   `replayToolFilePath`（commands.go），按与 live `extractFilePathFromEvent` 相同的优先级
   （修复读路径 → 输入 file_path → exit_plan_mode 输出信封 plan_file）从行内事实推导。
   测试：`TestReplayExitPlanModeCardCarriesThePlanFileLine`。
2. **fanout 卡 live 不报失败** —— 根因：`handleFanoutToolMessage` 两分支对「从未报告结束的 waiting
   任务」处理不一致：完成分支（live 先 started 后 completed）任其停在 waiting（卡片显示
   `Ran 1 tasks` 掩盖 dispatch 失败）；start 分支（replay 单条完成行）按 meta.Status 继承为 failed。
   修复：抽出 `settleUnreportedFanoutTasks` 两个分支共用——live 现在与 replay 一样如实显示
   `0 done, 1 failed ✗`。测试：`TestReducerFanoutFailedDispatchSettlesWaitingTasksBothOrders`。
3. **（顺带发现）replay 中 `calling exit_plan_mode` 双拼** —— 根因：clear_context 审批的锚点会话
   （`MinimalClearedResumeSnapshot` = [anchor user, gated assistant]）是给模型的上下文，reset 游标
   使旧行退出投影后整段作为新行落库；锚点 assistant 副本是普通可见行，回放连画两次，live 只画一次。
   修复：锚点 assistant 副本标记 `IsMeta`（"任何表面都没画过的行"），TUI
   `SurfaceTranscriptMessages` 与 web `handleChatMessages` 的 is_meta 过滤改为角色无关。
   测试：`TestMinimalClearedResumeSnapshotMarksGatedAssistantMeta`、
   `TestSurfaceTranscriptMessagesOmitsTheContextClearAnchorCopy`。

验证：全量 28 包测试 exit 0（`TestCharacterizationNetworkApproval` 为既有真网络 flake，单跑 3/3 过）、
3 平台 vet 绿、gofmt 干净、`pnpm build` 绿、deadcode 37 条不变。

### 实施后审计（2026-09-28）的 owner 裁决

审计修复中涉及语义取舍的点，owner 逐条裁决如下（均按所列方式落地）：

1. **Web「新对话」与项目内新会话未命名创建**：库里标题存会话 id，由第一条用户消息命名；列表接口对未命名会话返回空标题，
   由前端画「新对话」占位。不再把「New Chat」/项目名当标题入库（否则 `title=id` 命名规则让它们永远不被命名）。
2. **旧回合补建 legacy run 保留**：真实库 866 组中匹配已有 run 126 组、补建 740 条（计划预估的「0 补建」不成立：
   无计时的 run 仅约 190 个）。候选 run 另要求「尚未拥有任何消息行」。
3. **父 run 已不存在的子 run 连同整棵子树丢弃**（与 ON DELETE CASCADE 一致），其消息与事件保留、run 关联置空，丢弃数写入迁移日志。
4. **建 run 失败不再编造 run id**：`turn.Submit` 不再生成 `run-N`，该轮以执行器的原始错误结束、事件不带 run id；
   web 聊天在会话或 run 无法记录时以 `turn_withdrawn` 带原始错误把消息退回输入框。
5. **审计口径与界面一致**：成功的 `session_todo` 由计划卡呈现、不进审计与成本计数，失败的进；rewind-last 只针对最近一次成功的编辑。
6. **计划审阅的 run 是被审批暂停的 run 的子 run**，记在当前对话下（审阅者对话记录仍在自己的 worker 会话），token 计入对话用量。

### 二次审计（2026-09-28 晚，含并行分支引入的 v2/v3）

并行分支（会话模型隔离）把 `stateSchemaVersion` 升到 3：v2 修复「中间开发版本把库标成 v1」的情况，v3 新增
`fb_session_model_state`。二次审计发现并修复：

1. **已完成的 v1 库打不开**：v2 的「是否已是最终形态」比对用的是含 v3 表的当前 schema.sql，正确的 v1 库必然不匹配，
   被送去按 v0 重建，读不存在的 `legacy_fb_run_steps` 直接报错。改为比对时排除后续版本新增的表
   （`stateObjectsAddedAfterV2`），v3 的建表 DDL 直接取自 schema.sql 的声明文本，保证与新建库逐字节一致。
2. **中间形态 v1 库重建丢数据**：重建的拷贝函数只认 v0 列名，已被中间版本转换过的表会丢失可见性（撤回/修复行复活）、
   压缩/清空指针、执行计时、run 计时、文件存储键、action 会话、wait 的执行栅栏。拷贝函数改为逐列「有 v1 列用 v1 列，
   否则由 v0 推导」，`fb_run_steps` 不存在时只搬事件，并补上 `fb_session_model_state` 的拷贝。
3. **仅新增表的迁移也全量 VACUUM**：600MB 库升 v3 要 ~18 秒并阻塞其它进程。改为只在迁移释放了页时才 VACUUM（实测 0.36 秒）。
4. **会话尚未开始时的事件**：TUI 首条消息前的 `/compact` 等事件因外键写入失败而整张卡片消失。按 D16 的既定规则统一到
   存储层：会话行不存在时返回 `ErrSessionNotStarted`，TUI 与 gateway 只展示、不落库。
5. **web 聊天在会话归属校验前就执行斜杠命令**：客户端指定的会话在消息到达时即建立或拒绝；拒绝改用不写入日志的
   `turn_withdrawn`，不再把 `turn_completed` 写进别的 agent 的会话日志。
6. cron 任务的 `project_id` 未校验归属；并行分支遗留 4 个无调用方函数与 10 个未 gofmt 的文件，一并清理。

真实库演练（组合后的代码）：v0 副本与已完成的 v1 副本均迁到 v3，283 个会话的上下文哈希与 T0 基线逐行一致；
v1 副本不重建（事件游标不变）。TUI 实测跨重启 resume 的请求体：tools 与 messages 之前的全部字节一致、历史消息为严格前缀。
