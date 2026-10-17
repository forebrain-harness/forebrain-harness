# /migrate 增量更新 + TUI 迁移百分比进度条（与 compact 进度条同 UI）+ 仅增量数据做 Memory consolidation

> 批准后本文将落盘为 `docs/plan/MIGRATE_INCREMENTAL_PROGRESS_PLAN.md` 并在当前源码直接实施（不建 worktree、不 commit）。
> Planned at: dev@7155be9（工作树含无关的 assets/tokenizers 改动；drift 检查以本文 file:line 对照为准）。
> 本版已并入外部评审（deepseek）意见与 owner 两项裁决：**增量语义=包含"长大会话追加新消息"；升级过渡=纯回填（零语义变化）**。

## 1. 目标（Why this matters）

owner 三条硬性要求（原话）：

1. 「命令/migrate 必须支持增量更新，禁止每次执行都全量迁移。」
2. 「必须在tui主界面历史消息区域，composer上方展示百分比进度条」「百分比进度条的UI必须与compact的百分比进度条保持一致」。
3. 「migrate 的 Memory consolidation非常耗时，必须仅对增量数据进行Memory consolidation」。

owner 两项裁决（2026-10-16，计划模式内确认）：

- 增量语义**包含**：源会话文件（append-only jsonl）在迁移后长出新消息 → 再次 /migrate 把新消息追加进已迁移会话（按 message_id 去重，幂等）。migrated 会话从"不可变"变为"可增量续写"。
- 升级后首次运行对存量已迁移行**纯回填**：仍按今日语义 parse 一次并跳过、只写指纹账本；升级前源已长大的尾巴不导入（与今日完全一致，零语义变化）。

现状违背第 1 条的根因：`pkg/migrate/sessions.go:107-166` 的 `importUnitsCapture` 对每个 unit **先 parse（L123，整文件读 jsonl）再做已存在判定**。第二次运行把全部会话转录重新解析一遍，只是写层幂等跳过；而且一次 `/migrate` = preview（dry run 全量解析）+ run（再全量解析）**两遍全量**（`pkg/tui/commands.go:2250` PreviewMigrationAsync → 确认 → RunMigrationAsync）。多 GB 的 Claude/Codex 历史意味着每次执行都付出全量迁移代价。

现状违背第 2 条：迁移进度今天只是一条文本瞬态行（`pkg/tui/run.go:475` `renderer.RenderTransientStatus(transientSourceMigrate, "migrate: "+line)`），没有百分比、没有进度条。

## 2. 根因与现状证据链（file:line）

### 2.1 migrate 执行链

- `pkg/migrate/plan.go:324-404` `RunClaude`：discoverClaude 全量走目录 → 依次 importSessions / importMemories(+consolidateNow) / importSkills / importPlans / importMCP / importLSP / mergeInputHistory → `reportOrphanedMigratedSessions`；全局指令 note 在 **L399-401** 才 append 进 `report.Memories`。
- `pkg/migrate/plan.go:409-508` `RunCodex`：同构，sessions 与 plans 共享一次 parse（L434-453）：`importUnitsCapture` 的 `capture` map（只在实际 parse 的分支填充，`sessions.go:133-135`）交给 `extractCodexPlans(parsed, ...)`（`pkg/migrate/codex_assets.go:393`，经 `sessionText`（:464）抽取 plan 块 → `writeExtractedPlan`（:488-518，identical→up-to-date / missing→write / diverged→不覆盖 的三态幂等））。**codex 的 plans 完全依赖 capture：跳过 parse 的 unit 在 plans 类目下将失去 outcome 与"用户删了 plan 文件后重跑自愈"的能力。claude 的 plans 是文件复制（`claudeData.Plans`，claude.go:41-43，discovery 直读 plans/ 目录），与 session parse 无关。**
- `pkg/migrate/sessions.go:107-166` `importUnitsCapture`：循环体 L119-164，**L123 先 `parse(ctx, unit.Path, ...)`**，L139（dry run）/L157（run）才判定是否已存在。判定链：`sessionOutcomeFor`（L320-363）→ `sessionOwner`（L489-501，`SELECT agent_id, origin, COALESCE(project_id,'') FROM fb_sessions WHERE id=?`）。
- 写路径：`importOneSession`（L389-438，每会话独立事务）→ `insertSession`（L513-536，fb_sessions 行，origin='migrated'，memory_mode='disabled'）→ `insertRows`（L558-597，fb_messages 逐行插，**message_id = 源记录 uuid 原样映射**，见 `pkg/migrate/parse.go:67`（`MessageID string // source uuid`）与各映射点 389/419/450/468/479/505/534/782/898——parse 层对 uuid 无非空守卫，直接 `MessageID: rec.UUID`；codex 侧 `pkg/migrate/codex_parse.go:395-669`，含 `reasoning-<ts>`、`compacted-<n>-<window>` 等合成 id，皆为源内容的纯函数，重解析稳定）。
- rebound 治愈：`rebindMigrated`（sessions.go:440-485）——已迁移但无 project 的行补绑项目。
- 进度事件现状：**只有 sessions 阶段发 Done/Total**（`sessions.go:116`）；memories/skills/mcp/plans/history/lsp 只发 Detail（`assets.go:161/397/487/608/1025/1182`、`lsp.go:234`、`codex_assets.go:153/414/534`）；consolidating 只有 Detail（`plan.go:534`）。百分比公式必须按此事件形状设计。
- discovery 成本：`DetectSources`（picker 行描述）本身走一遍 `discoverClaude`（`pkg/tui/commands.go:2178` → `source.go:47` → `claude.go:101-107` `claudeSourceCounts` → `discoverClaude`），加上 preview 与 run 各一趟 = 每次 /migrate **三趟目录 walk**；每趟对每个 jsonl 做 head 扫描（`claude.go` `scanSessionHead`、`codex.go:170` `scanCodexHead`）。这批不能省：claude 的 plan 归属依赖 head 记录里的 Slug/Cwd（`assets.go:1056-1070` `discoverPlans`）。增量改造后的耗时从"与总字节数成正比"降为"与文件数成正比"。
- `pkg/migrate/source.go:329-338` `Progress` 结构只有 `Stage/Done/Total/Detail`，无百分比。
- `pkg/state/schema.sql:91-115`：fb_messages **没有 (session_id, message_id) 唯一索引**（只有 visibility/run 两个普通索引）→ 增量去重不能靠约束，用查询集合。
- 状态库迁移梯子：`pkg/state/schema_migrations.go:21` `const stateSchemaVersion = 14`；L51-55 注册表；L1634-1641 逐级 apply。新增表 = v15 迁移 + `schema.sql` 同步（owner 既有裁决：状态库结构改动必须迁移旧库）。additive 表的样板：`migrateStateV13ToV14`（L639 起，`CREATE TABLE IF NOT EXISTS` 幂等）。
- `/migrate` 仅 TUI 面（`pkg/turn/slash.go:100` `AllowedSurfaces: []Surface{SurfaceTUI}`）→ 无 gateway parity 工作。`runDisallowedCommands` 含 migrate（slash.go:117）→ 迁移只与空闲事件循环共存。
- 其他类目写层已幂等（memories `assets.go:193-218` note 存在即 skip；skills `assets.go:389-442` up-to-date；MCP `assets.go:518-526` 同名 skip；plans 三态幂等如上）——它们的重跑成本只是读源文件（小），本期不动（codex plans 的 capture 依赖除外，见 4.3）。

### 2.2 TUI 进度链

- `pkg/tui/chat_slash.go:931-949` `runMigrationAsync`（goroutine + notifyUI）；`:952-965` `PreviewMigrationAsync`（dry run 同样发进度）；`:971-987` `migrateProgressSink`（200ms 节流）→ `MigrationProgressMsg{Stage,Done,Total,Detail}`（`pkg/tui/notify.go:214-221`，注释明写进度 "never as transcript history"）。
- `pkg/tui/run.go:462-506` 主循环 notify 处理：L467-475 进度→瞬态行文本；L476-495 preview→ConfirmInfo 确认框（确认后 L493 又设 "migrate: importing…" 瞬态行再 `migration.Run()`）；L496-506 done→`migrationReportFrame`（`pkg/tui/commands.go:624-631`，replay 路径复用同一构造器，无 StepID）。
- **compact 进度条 UI（必须逐字复用其绘制函数）**：`pkg/tui/render.go:5726-5750` `renderCompactProgressBar(percent, phase, width)`（渐变填充 `compactBarRamp`、扫光 `compactBarShimmer`、粗体 `NNN%` 标签）；宽度 `compactProgressBarWidth`（5705-5711）；卡片布局 `renderMemoryCompact`（5633-5665）：running = header 行（spinner 标记+标题+summary）+ **bar 独占一行**；final = 内容体。数据通道 `Frame.Progress int`（`pkg/tui/reducer.go:123-124` 注释 "percentage, 0 to 99"），样板帧 `runningCompactionFrame`（reducer.go:185-199，`Kind: FrameMemoryCompact, StepID: compactionStepID(...)`）。
- **新 FrameKind 的全部承重点（缺一处即坏，均已核实）**：
  1. `pkg/tui/reducer.go:5137-5142` RenderFrame 路由：`if f.Kind == FrameFanout || FrameTool || FrameMemoryCompact || FrameSkillInstall → r.vm.replaceOrAppendBlock(f)`，**否则落入 append 兜底**——不加 FrameMigration 则每个进度 tick 追加一张新卡；
  2. `pkg/tui/reducer.go:3113-3136` 渲染分派 switch：`case FrameMemoryCompact: sr.renderMemoryCompact(f)`，**default → renderCompactFrame(system)**——不加 case 则根本不画 bar；
  3. `pkg/tui/reducer.go:3869-3883` `hasLiveBlockLocked`：只认 `FrameMemoryCompact && !Final` 与 Fanout——不加则 120ms 动画 ticker 不启动，spinner/shimmer 静止，与 compact 卡不一致；路由处对 MemoryCompact/Fanout 调 `syncLiveBlockAnimationLocked`（5132/5140）需同步加 FrameMigration；
  4. `pkg/tui/reducer.go:2534、2684、2826` 等 Kind 分类点（折叠/点击/摘要豁免）按 compact 卡同等语义接入；
  5. `pkg/tui/tty_session.go:1229-1236` `replaceOrAppendBlock` 本体是通用 Kind+StepID 匹配循环，**预期零改动**，但列入 scope 供实施时核对。
- handleMigrate 全流程：`pkg/tui/commands.go:2164-2260`（源 picker → 源目录输入 → 范围选择 → PreviewMigrationAsync → 确认 → RunMigrationAsync）。

### 2.3 Memory consolidation 现状（第 3 条要求的根因）

- 触发点：`pkg/migrate/plan.go:360-362`（RunClaude）与 `:463-465`（RunCodex）——`if !opts.DryRun && len(report.Memories) > 0 { report.Consolidation = consolidateNow(...) }`。**`len(report.Memories)` 数的是 outcome 行数（含全部 skipped 的项目行），不是本次实际写入的笔记数**——零新增的增量迁移也会触发整趟 consolidation。
- **claude 侧还有顺序缺陷**：门槛在 L360 先判，而全局指令 outcome（其 `Written` 只在真正写入时 append，`assets.go:1152` 附近 `importClaudeGlobalInstructions`）在 L399-401 才进 `report.Memories`——本次唯一新增是改过的 `~/.claude/CLAUDE.md` 时，ΣWritten 判 0、实际写了 1。codex 侧顺序正确（L462 在 L463 前）。
- `consolidateNow`（plan.go:526-540）→ `opts.Consolidate(ctx, "migration")`，回调在 `pkg/tui/chat_slash.go:918-923` 注入：`runner.ConsolidateMemoriesNow`。
- `pkg/run/memory_llm.go:367-385` `ConsolidateMemoriesNow` → `p.Run(ctx, sessionID)`；`pkg/memory/pipeline.go:72-110` `Pipeline.Run` = **scope 准备循环（MkdirAll+seed，76-85）** + RateLimitGuard（95） + prune stage1 产物 + **`ClaimStage1JobsForStartup` 认领原生会话 rollout 做 LLM 抽取**（`pkg/memory/jobs.go:140+`，`memory_source IN ('tui','webchat') AND memory_mode=ThreadMemoryEnabled`）+ 逐 scope `runStage2`。迁移触发的这趟 pass 会顺带把**与本迁移无关的原生 rollout** 也抽取一遍——耗时且不是"本迁移的增量数据"。（迁移来的会话本身安全：`insertSession` 写 `memory_mode='disabled'`，sessions.go:528，不满足认领条件。）
- `runStage2`（pipeline.go:348-424）：diff 机制 = memory 工作区 git 仓库的 `git status` + `git diff`（`pkg/memory/workspace.go:114-145`），无变化时快速成功（pipeline.go:418-424），pass 结束提交基线（workspace.go:73-76）→ **stage2 天然按"自上次 consolidation 以来的变更"增量**；其输入取自 Store（`SelectStage1ForPhase2`，pipeline.go:368）与工作区文件，**与本趟 stage1 无内存耦合**（抽取 `runPhase2Scopes` 已验证安全）。首迁把整个导入笔记库一次性变成未处理变更（数据本身就是增量，属固有成本，受 `maxWorkspaceDiffBytes` 截断与 agent 循环约束）；真正违背要求的是：零新增的重复执行也整趟跑 + 混入原生 rollout 的 stage1 抽取。
- 写入计数来源已存在：`MemoryOutcome.Written []string`（`pkg/migrate/assets.go:27`；项目笔记 199/211、全局指令 `importClaudeGlobalInstructions`/`importCodexGlobalInstructions` 均只在真正写入时 append）。

### 2.4 将被本计划修订的既有测试（必须随契约更新并注明理由）

- `pkg/migrate/sessions_test.go:674-677`：三跑断言 `Reason == "already migrated"` → 新快路径下未变 unit 的 Reason 变为 "already imported; unchanged since last run"（Status 仍 StatusSkipped）；随契约更新断言。
- `pkg/migrate/plan_test.go:39-41`：`plan.Sessions+plan.Subagents == migrated+skipped` 与 MessageRows 不变量 → 随 Updated 状态扩写（Updated 计入 preview 的 "to update"，MessageRows 加 delta 行数）。

## 3. 行为变化矩阵

| 场景 | 旧行为 | 新行为 | 备注 |
|---|---|---|---|
| 第二次 `/migrate`（源无变化） | 全量重新解析所有转录，报告全部 "already here"，耗时段=首迁 | **不 parse 任何未变转录**（codex plans 需要的除外，见 4.3）；耗时从与总字节成正比降为与文件数成正比（三趟 discovery head 扫描保留：picker 计数/preview/run）；报告计入 "already here (unchanged)" | 核心诉求 |
| 源新增会话/子代理转录 | parse 后按新 unit 导入 | 同样导入（账本 miss → 全量导入路径不变） | |
| 源会话文件被追加（append-only jsonl 长大） | 整个跳过（"already migrated"），**新消息永远进不来** | parse 该 unit 后**仅追加新 message_id 的行**（Status="updated"，报告 "N updated (M new messages)"），并更新 updated_at/title/compact 边界 | **owner 裁决：增量包含追加**；migrated 会话可增量续写 |
| 账本缺失但会话已迁移（升级后首跑） | 全量 parse 后 skip | 仍 parse 一次、按今日语义 skip、**纯回填账本**（当前文件指纹为基线）；升级前源已长大的尾巴不导入 | **owner 裁决：纯回填**——与今日完全一致，仅换取此后走快路径 |
| codex 会话未变、其 plan 文件被用户删除 | 重跑自愈（全量 parse 顺带重抽取） | 账本记录该 unit 的 plan 名单；文件缺失 → parse 该 unit 重抽取（自愈保留）；文件齐全 → 不 parse，报 up-to-date | 评审修正：保住 codex plans 的 capture 依赖与自愈契约 |
| 已迁移行缺 project（rebound） | parse 取 Cwd 后补绑 | 不变（rebound 路径需要 Cwd，不走快路径） | |
| dry run（preview 确认框） | 全量解析后给数字 | 同样走快路径：未变 unit 不 parse；长大 unit 报 "would append N messages" | preview 也快 |
| 迁移进行中的 TUI | composer 上方一条文本瞬态行 `migrate: sessions 12/345 · xx.jsonl` | **compact 同款百分比进度条卡片**（header 行 + bar 行，`renderCompactProgressBar` 原函数），随进度原地刷新 | owner 指定 UI 一致 |
| 迁移完成/失败/取消 | 瞬态行消失，报告/错误帧追加 | 终态帧（报告体作为 FrameMigration final 卡内容）同 StepID **原位替换**进度卡——live 与 replay 各恰好一块报告；replay 帧（commands.go:624）保持无 StepID | 评审修正：消除 live 两块/replay 一块的不对称 |
| Memory consolidation 触发门槛 | `len(report.Memories)>0`（outcome 行数，全 skip 也触发）→ 每次 /migrate 整趟 Pipeline.Run（prune + 原生 rollout stage1 抽取 + stage2）；claude 侧门槛先于全局指令写入判定 | **仅当本次迁移实际写入 ≥1 条笔记（ΣWritten>0，含全局指令）才跑**，门槛移到两个类目都写完之后；且只跑 stage2 工作区整合（保留 scope 准备与 RateLimitGuard；不 prune、不认领任何 rollout） | 核心诉求 3；评审修正顺序缺陷 |
| 零新增的增量迁移 | 照样触发 consolidation（耗时 LLM pass） | 不触发；报告一行 "consolidation: not run — no new notes" | |
| 写入 N 条新笔记的迁移 | 整趟 Run（含无关 stage1） | 仅 stage2，整合的正是本次新写笔记（git 工作区 diff=自上次基线的增量） | 首迁全库一次性整合为固有成本，文档写明 |
| 正常使用中的后台记忆管线（LaunchMemoryStartup） | Pipeline.Run 全量 | **不变**（只有迁移注入的回调改走 stage2-only 入口） | 不碰后台语义 |
| 斜杠输出进模型 | 不进 | 不变（报告仍只是 UI 帧） | 既有红线 |

刻意不改：memories/skills/mcp/lsp/history 各类目内部逻辑与报告格式（仅加 updated 计数行）；`runDisallowedCommands`；`/migrate` 的 picker 流程与确认框；gateway（命令仅 TUI）；三趟 discovery 的去重合并（记 Maintenance）。

## 4. 修复设计

### 4.1 迁移账本（状态库 v15）

新表（`pkg/state/schema.sql` 追加 + `schema_migrations.go` 注册 v15，样板 `migrateStateV13ToV14`）：

```sql
CREATE TABLE IF NOT EXISTS fb_migration_units (
  agent_id TEXT NOT NULL,
  source TEXT NOT NULL,             -- 'claude' | 'codex'
  target_id TEXT NOT NULL,          -- cli-<uuid> / cli-<uuid>-<agentID>
  source_path TEXT NOT NULL,
  source_bytes INTEGER NOT NULL,
  source_mtime_ms INTEGER NOT NULL,
  message_count INTEGER NOT NULL DEFAULT 0,
  -- plan_names is the comma list of plan names extracted from this unit
  -- (codex only; empty for claude units and units that held no plans).
  plan_names TEXT NOT NULL DEFAULT '',
  imported_at_ms INTEGER NOT NULL,
  PRIMARY KEY(agent_id, target_id)
);
```

- `stateSchemaVersion` 14→15；迁移体只有 `CREATE TABLE IF NOT EXISTS`（幂等，空库/旧库都安全）。
- 读写封装放 `pkg/migrate/ledger.go`（新文件）：
  - `loadUnitFingerprints(ctx, db, agentID, source) map[string]unitFingerprint`（含 plan_names，import 开始时一次查询）；
  - `recordUnit(ctx, execer, agentID, source, unit, messageCount, planNames)`（`*sql.Tx` 或 `*sql.DB` 都能接，`interface{ ExecContext }`）。
- `pkg/migrate` 已 import `pkg/state`（sessions.go:13），无包图变化（不需要重生成 graph.json）。

### 4.2 discovery 记录 mtime

- `pkg/migrate/claude.go`：`claudeSession` 增加 `MtimeMs int64`（walk 处 `info.ModTime().UnixMilli()`，info 已在手）；`claudeSubagent` 同（`subagentBytes` 处的 `os.Stat` 同样拿 mtime，函数改名或加返回值）。
- `pkg/migrate/codex.go`：`codexRollout` 增加 `MtimeMs int64`（`discoverCodexRollouts` L166-180 处 `info` 已在手）。
- `sessionUnit` 增加 `MtimeMs int64`，两源 `units` 构造处填充。

### 4.3 sessions 导入判定（`importUnitsCapture` 重排，判定树穷尽）

每 unit 判定顺序（run 与 dry run 共用；`sessionOwner` 先于一切 parse）：

0. **codex plans 预检**（仅 `opts.wants("plans")` 且源为 codex 时参与快路径放行条件，见下）。
1. **快路径**（账本命中 (agent_id, target_id) 且指纹一致）：
   - `sessionOwner` 返回 本 agent + origin='migrated' + project 非空，**且**（非 codex-plans 场景 或 账本 plan_names 为空 或 plan_names 中每个 plan 文件在 plan store 中仍存在）→ **不 parse**，outcome `Status=StatusSkipped, Reason="already imported; unchanged since last run"`，Title 取自 stage 开始时批量预取的 `fb_sessions` 标题表（一条 `SELECT id,title FROM fb_sessions WHERE agent_id=? AND origin='migrated'`）。codex 场景 plan 文件齐全时同步产出对应 `PlanOutcome{Status:"up-to-date"}`，保证二次运行 `report.Plans` 与首次一致。
   - 账本命中但上述条件不满足（行被用户删除 / project 空 / codex plan 文件缺失）→ 落入 2 的 parse 路径（自愈：plan 缺失时 parse 重抽取；行删除时全量重导）。
2. **指纹不符（源文件变化/长大）**：parse 后若会话已存在（本 agent、migrated、bound）→ `appendSessionDelta`（4.4）→ outcome `Status=StatusUpdated, Messages=追加行数`，事务内 upsert 账本（含重抽取的 plan_names）。若会话行不存在 → 原路径 `importOneSession` 全量导入 + 记账本。
3. **账本 miss（新 unit 或升级前旧导入）**：parse 后 `sessionOutcomeFor` 维持今日语义：
   - absent → `importOneSession`（事务内加账本 upsert）；
   - present+migrated+bound → skip "already migrated" + **纯回填账本**（当前 stat 为基线；owner 裁决：升级前已长大的尾巴不导入，与今日一致；dry run 不写）；
   - rebound/owned/failed → 不变，不记账本。
   - codex：本趟 parse 的 capture 照常填充，plans 正常抽取；抽取完成后把 plan 名单写进该 unit 的账本行。

约束：
- 账本 upsert 与会话写同事务（`importOneSession` 已开事务，`appendSessionDelta` 自开事务）；失败回滚则账本不前进，下次重做该 unit——幂等。
- 快路径的 `sessionOwner` 逐 unit PK 查询（数千次点查，SQLite µs 级），不做 IN 批量（除非测试显示慢，作为可选优化）。
- delta 去重键 = message_id；集合成员测试要求键非空——`MessageID == ""` 的行不参与 delta 追加（这是去重键域的定义，不是防御分支；parse 层无 uuid 非空守卫，parse.go:389 等直接映射 rec.UUID），全量导入不受影响。
- `reportOrphanedMigratedSessions`（sessions.go:280-313）依赖 outcome.TargetID 填 `seen`——快路径同样产出带 TargetID 的 outcome，孤儿检查行为不变。

### 4.4 `appendSessionDelta`（长大会话的增量追加）

```
existing := SELECT message_id FROM fb_messages WHERE session_id=?   -- 一次查询建 set
newRows  := parsed.Rows 中 MessageID != "" 且不在 set 的行（保持转录顺序）
tx:
  插入 newRows（复用 insertRows 的绑定与 boundary 逻辑，抽出可过滤版）
  UPDATE fb_sessions SET updated_at=max(updated_at, parsed.LastSeen),
         title=parsed.Title(非空且变化时) WHERE id=? AND agent_id=? AND origin='migrated'
  boundary：parsed.Rows 中最后一个 IsBoundary 行；若它是新插入行 → 更新 compact_boundary_message_id；若已存在 → 指针本就正确，不动
  账本 upsert（新指纹+message_count+plan_names）
commit
```

- 不删行（源里删掉的记录 DB 保留——与"追加式转录"现实一致）。
- 幂等：重跑同一长大文件 → 账本已新指纹 → 快路径；账本丢失 → message_id 集合去重兜底。

### 4.5 报告与 Plan 文本

- `SessionOutcome` 新 `StatusUpdated = "updated"`；`Report.SessionCounts`/`Text`（plan.go:545+）会话行加 `· N updated (M new messages)`；dry run 措辞 "to update"。
- `Plan`（plan.go:76-98）加 `Updated/UpdatedMessages`，`reportToPlan` 折算；preview 确认框数字随之反映增量；`plan_test.go:39-41` 的 plan/run 计数不变量随 Updated 扩写。
- codex 快路径补发的 `PlanOutcome{up-to-date}` 让二次运行 `report.Plans` 与首次一致（评审要求的行为不变量）。

### 4.6 百分比（`migrate.Progress` 加 `Percent int`，按实际事件形状设计）

已核实：只有 sessions 阶段发 Done/Total（sessions.go:116），其余类目只发 Detail（§2.1）。因此：

- 新 `pkg/migrate/progress.go`：`percentPlan` 按**启用类目**（`opts.wants`）在**执行顺序**上静态分片：`sessions [0,90)`，其余启用类目（memories、consolidating、skills、plans、mcp、lsp、history）**均分 [90,100)**。
- wrapper 是**阶段迁移驱动**的状态机（非 done/total 插值到所有阶段）：
  - 收到 checkpoint：stage 变化 → 上一阶段收片（percent 跳到其 hi）、新阶段开片（percent 落其 lo）；
  - 仅 sessions 阶段在片内用 `lo + (hi-lo)*done/total` 插值（它有真实计数）；
  - `discovering` 恒 0；运行中 clamp 99；run/dry-run 结束由终态帧表达 100%。
  - 类目被跳过（`opts.wants` 为假）不占分片；某类目中途失败提前返回时，Run* 在该类目返回处标记收片（bar 不停滞）。
- `RunClaude`/`RunCodex` 用 wrapper 包住传入的 `progress` 回调补充 `Percent`，importer 签名零改动。
- 权重是启发式（sessions 占绝对时长），文档写明；不承诺 ETA。

### 4.7 TUI 进度卡（compact 同款 UI）

- `MigrationProgressMsg`（notify.go:216-221）加 `Percent int`；`migrateProgressSink`（chat_slash.go:983-985）透传。
- `pkg/tui/reducer.go` FrameKind 加 `FrameMigration`；`pkg/tui/render.go` 加 `renderMigration`：**逐字复用** `renderCompactProgressBar(f.Progress, r.spinnerPhase, compactProgressBarWidth(termWidthOrDefault()))` 与 `renderMemoryCompact` 的双行布局（header 行 = spinner 标记+标题+Summary；bar 独占一行；final 渲染内容体）。
- **接线全部承重点（§2.2 已列，缺一即坏）**：reducer.go:5137-5142 RenderFrame 路由加 FrameMigration（否则每 tick 追加新卡）；reducer.go:3113-3136 渲染分派加 case（否则不画 bar）；reducer.go:3869-3883 `hasLiveBlockLocked` 加 `FrameMigration && !Final`（否则动画 ticker 不启动、shimmer 静止）并在路由处对 FrameMigration 调 `syncLiveBlockAnimationLocked`；reducer.go:2534/2684/2826 分类点按 compact 卡同等语义；tty_session.go:1229-1236 预期零改动（通用 Kind+StepID 匹配），实施时核对。
- `run.go:462-506` 改造：
  - `MigrationProgressMsg` → `renderer.RenderFrame(Frame{Kind: FrameMigration, StepID: "migrate-progress", Title: 运行标题, Summary: "<stage 标签> · <done>/<total> · <detail>", Progress: percent})`（live 卡在 transcript 底部原位刷新 = composer 上方）；
  - 删除 `transientSourceMigrate` 瞬态行的使用（run.go:475/477/493/499 与 commands.go:2244 的 "planning the import…" 行——preview 阶段的进度同样画卡）；
  - **preview 与 run 共用同一 StepID（刻意）**：一张卡从 "Previewing migration" 无缝变形为 "Migrating <source>"，run 的首个 checkpoint 原位替换 preview 卡；
  - `MigrationDoneMsg` → **报告体作为 FrameMigration final 卡**（同 StepID 原位替换进度卡，Final:true，Title "Migration complete"，内容体=报告文本）——live 与 replay 各恰好一块报告；失败：FrameError 同 StepID 原位替换（错误文本）；replay 路径 `migrationReportFrame`（commands.go:624-631，无 StepID）保持不变，同会话 replay 两条迁移报告不会互吞帧。
- 确认框（ConfirmInfo）与进度卡并存：进度卡画在 modal 之下，与旧瞬态行行为等价。

### 4.8 Memory consolidation 仅限增量数据

1. **门槛改为真实写入数，且移到写入全部完成后**：`Report` 加小助手 `writtenMemoryCount()`（Σ `MemoryOutcome.Written`，含全局指令 outcome）。RunClaude 的 consolidation 块**从 L360 移到 L399-401（importClaudeGlobalInstructions append 之后）**（修复"唯一新增是 CLAUDE.md 时判 0"的顺序缺陷）；RunCodex 的门槛同样在全局指令 append 之后（现状顺序已对，只换条件）。条件由 `len(report.Memories) > 0` 改为 `writtenMemoryCount() > 0`。未触发时 `report.Consolidation = "not run — no new notes"`（报告文本一行，用户可见这个决定）。
2. **stage2-only 入口**：
   - `pkg/memory/pipeline.go`：把 `Run` 里逐 scope 的 `runStage2` 循环抽成 `runPhase2Scopes(ctx, settings)`（已验证安全：stage2 输入取自 Store 与工作区文件，与本趟 stage1 无内存耦合）；`Run` 保持现语义（scope 准备 + guard + prune + stage1 + phase2）；新增 `func (p *Pipeline) ConsolidateWorkspace(ctx context.Context) error` = Enabled 检查 + **scope 准备循环（MkdirAll+seed，76-85）+ RateLimitGuard（95）** + `runPhase2Scopes`——只跳过 prune 与 stage1 认领（这两样不是本迁移的增量数据；seed 与 guard 保留，理由：prepareMemoryWorkspace 只兜 MkdirAll，seed 无其他兜底；guard 是账号级保护）。
   - `pkg/run/memory_llm.go`：新增 `(r *Runner) ConsolidateMemoryWorkspace(ctx) error`（镜像 `ConsolidateMemoriesNow` 的 nil/Enabled/pipeline 检查，调 `ConsolidateWorkspace`）。
   - `pkg/tui/chat_slash.go:918-923`：迁移注入的 `Consolidate` 回调改调 `ConsolidateMemoryWorkspace`。`ConsolidateMemoriesNow` 本体与 `LaunchMemoryStartup` 不动（正常使用语义不变）。
3. stage2 的 git 工作区 diff 机制（pipeline.go:406-424）本身就是"自上次基线以来的增量"，配合门槛 1 即满足"仅对增量数据"：不写不跑；写了只整合新写的。首迁整库一次性整合是数据固有体量，不在本期拆批（记 Maintenance）。

## 5. Scope

**In scope**：`pkg/state/schema.sql`、`pkg/state/schema_migrations.go`（v15）、`pkg/migrate/{ledger.go,progress.go,sessions.go,claude.go,codex.go,codex_assets.go(仅 plan 名单回填),plan.go,source.go,doc.go}`、`pkg/memory/pipeline.go`（抽出 runPhase2Scopes + ConsolidateWorkspace）、`pkg/run/memory_llm.go`（ConsolidateMemoryWorkspace）、`pkg/tui/{notify.go,run.go,reducer.go,render.go,tty_session.go(仅核对),chat_slash.go,commands.go}`、上述文件的 `_test.go`、`FOREBRAIN.md`/`AGENTS.md` 中 `/migrate` 段落补增量语义与 consolidation 门槛、`docs/` 站点 migrate 相关页（若存在）。

**Out of scope（明确不碰）**：
- `pkg/migrate/assets.go` 各轻量类目内部、`codex.go` 的 sqlite 快照跳过（`loadCodexState` 每次复制 state_5/memories_1——量级小，留作后续；已记 Maintenance）。
- 三趟 discovery（picker 计数/preview/run）的合并复用（记 Maintenance）。
- `Pipeline.Run` / `LaunchMemoryStartup` / `ConsolidateMemoriesNow` 既有语义（后台记忆管线不动）。
- 首迁大记忆库的 consolidation 分批。
- gateway/web 面（`/migrate` 仅 TUI）。
- slash 注册表与 inline 参数（FOREBRAIN.md 所述 `/migrate claude --dry-run` inline form 与 `slash.go:100 SupportsInlineArgs:false` 不符，属文档漂移，另期处理）。
- compact 进度条自身实现（只复用不修改）。

## 6. Steps（每步带验证）

1. **状态库 v15**：schema.sql 加表；`stateSchemaVersion=15`；注册 `migrateStateV14ToV15`（仅 CREATE TABLE IF NOT EXISTS）。
   验证：`CGO_ENABLED=1 go test -tags fts5 ./pkg/state -run 'Schema|Migration' -count=1` 全绿（含既有"v14 库迁移到当前版"用例自动覆盖 v15）。
2. **discovery mtime**：claude/codex discovery 与 `sessionUnit` 加 `MtimeMs`。
   验证：`CGO_ENABLED=1 go test -tags fts5 ./pkg/migrate -count=1`。
3. **账本 + 判定树 + delta 追加 + codex plans 保活**：ledger.go、importUnitsCapture 重排、appendSessionDelta、StatusUpdated、plan_names 回填与快路径 up-to-date 补发、Report/Plan 文本、既有测试随契约更新（sessions_test.go:674-677、plan_test.go:39-41）。
   验证：新增用例（见 §7）+ `CGO_ENABLED=1 go test -tags fts5 ./pkg/migrate -count=1`。
4. **百分比**：progress.go + Progress.Percent + Run* wrapper（阶段迁移状态机）。
   验证：percentPlan 单测（分片边界、阶段切换收片/开片、sessions 插值、discovering=0、clamp 99、跳过类目不占片、失败类目收片）。
5. **consolidation 仅增量**：门槛移位 + writtenMemoryCount + runPhase2Scopes 抽取 + ConsolidateWorkspace（含 seed+guard）+ ConsolidateMemoryWorkspace + 回调切换 + 报告行。
   验证：`CGO_ENABLED=1 go test -tags fts5 ./pkg/memory ./pkg/run ./pkg/migrate -count=1`；新用例见 §7。
6. **TUI 进度卡**：FrameMigration + renderMigration + 四处承重点接线（路由/分派/动画/分类）+ run.go 改造 + 瞬态行清除。
   验证：`CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -count=1`；新增用例：进度卡渲染断言 bar 字节序列与 compact 一致（同一 percent/phase/width 下 `renderCompactProgressBar` 输出逐字节相同）、同 StepID 原位替换（进度卡→终态报告卡各一块）、replay 帧无 StepID。
7. **回归**：`FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./... -count=1` + `go vet ./...` + `gofmt -l cmd pkg third_party` 为空。
8. **tmux 真机验收**（见 §8）。

## 7. 测试计划

- `pkg/state`：v15 迁移（旧库升级后表存在且可写；二次打开幂等）——沿用 db_test.go 既有迁移用例模式。
- `pkg/migrate`：
  - **黑盒"不再全量解析"证明**：fixture 首迁成功后，把某会话 jsonl 原地覆写为垃圾字节但**保持 size 与 mtime**（`os.Truncate`+写等长垃圾+`os.Chtimes` 恢复），二跑若重新 parse 必然 failed；断言二跑该 unit 为 skipped("already imported; unchanged") 且 `fb_messages` 行数不变。
  - 增量追加：首迁后向 fixture 会话追加 2 条合法记录（mtime 推后），二跑断言 `Status=updated`、`fb_messages` 恰好 +2、`fb_sessions.updated_at` 前进、标题更新；三跑断言全 skipped-unchanged（幂等闭环）。
  - 新 unit：二跑前放新会话文件 → 正常导入。
  - 账本纯回填：模拟升级前旧库（手插 fb_sessions migrated 行、无账本行）→ 跑一次按今日语义 skip+回填（源文件在首迁后已 append 的行**不**导入——钉死 owner"纯回填"裁决）→ 再跑走快路径。
  - **codex plans 保活**：含 plan 块的 codex fixture 首迁 → 二跑 `report.Plans` 与首跑一致（up-to-date）；用户删除某 plan 文件后三跑 → 该 plan 重抽取（自愈）；plan 文件被改成不同内容 → diverged 不覆盖（三态幂等不变）。
  - dry run：不写任何行、不写账本；preview 数字含 "to update"。
  - codex 路径至少一条追加用例（共用 importUnitsCapture，重点在合成 message_id 的稳定性）。
  - **consolidation 仅增量**：fixture 含记忆文件 → 首迁 `report.Consolidation=="consolidated"`（Consolidate 回调用假实现计数）；同 fixture 立即二跑（零新增）→ 回调**零调用**、`Consolidation` 为 "not run — no new notes"；向源新增一条记忆再跑 → 回调恰好一次；**仅改 `~/.claude/CLAUDE.md` 再跑 → 回调恰好一次**（钉死门槛移位修复）。
- `pkg/memory`：`ConsolidateWorkspace` = scope 准备 + guard + phase2，不调 ClaimStage1JobsForStartup / PruneStage1Outputs（store 层 stub 计数断言为零）；既有 `Run` 用例全绿证抽取无行为变化。
- `pkg/run`：`ConsolidateMemoryWorkspace` 镜像检查（disabled → ErrMemoriesDisabled；pipeline 不可用 → error；正常 → 调 ConsolidateWorkspace）。
- `pkg/tui`：MigrationProgressMsg 渲染出 FrameMigration 卡（StepID、Progress 透传、bar 与 compact 字节一致）；done 帧同 StepID 原位替换且仅一块；replay 报告帧无 StepID。模式参照 run_test.go 现有 notify 用例。

## 8. tmux 真机验收（必做）

```bash
go build -tags fts5 -o ./build/bin/forebrain ./cmd/forebrain
# 隔离环境：临时 FOREBRAIN_HOME + 临时项目目录 + CLAUDE_HOME 指向造好的 ~/.claude fixture
tmux new-session -d -s fbt -x 120 -y 42 'cd <临时项目> && FOREBRAIN_HOME=<临时home> CLAUDE_HOME=<fixture .claude> ./build/bin/forebrain; echo EXIT_CODE=$?; sleep 300'
```

场景（逐屏 `capture-pane -p` 取证）：
1. **进度条**：fixture 含足量会话（或单大文件），驱动 `/migrate` → picker → 确认；迁移中 capture 可见 composer 上方 compact 同款 bar（渐变 + `NNN%`，shimmer 在动）；与 `/compact` 触发的 bar 并排比对字节风格一致。
2. **增量**：同一环境立刻再跑 `/migrate` → 快速完成（无逐会话解析阶段），报告 "already here"，无长进度阶段。
3. **更新**：向 fixture 会话文件追加记录后再跑 → 报告 "1 updated (2 new messages)"。
4. **中断自救**：迁移中 Ctrl+C → EXIT_CODE 干净；重跑正常（账本未前移的 unit 重做）。
5. **consolidation 增量**：场景 2 的零新增二跑报告中 consolidation 行为 "not run — no new notes"（且全程无 consolidating 进度）；新增一条记忆的三跑只见一次 consolidating 阶段。
6. **回归**：无 CLAUDE_HOME/CODEX_HOME 时 `/migrate` 报 "No migratable agent"，TUI 其余功能不受影响；正常会话后触发一次后台记忆管线行为不变（`/compact` 或 startup 路径不回归）。

## 9. Done criteria（机器可查）

- `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./... -count=1` 全绿；`go vet ./...` 零输出；`gofmt -l cmd pkg third_party` 为空。
- §7 黑盒用例通过：未变化转录第二次执行零解析（损坏文件不被触碰即证明）。
- codex plans 三态：二次运行 report.Plans 与首次一致；删文件自愈；diverged 不覆盖。
- 零新增迁移的 Consolidate 回调零调用（含"仅改 CLAUDE.md 必触发一次"的反向钉死）。
- tmux §8 场景 1-6 取证通过。
- `git status` 改动范围与 §5 Scope 一致，留工作区不 commit。

## 10. STOP conditions

- 发现 fb_messages 存在与 source uuid **不稳定**的 message_id（同一文件重解析 id 漂移）→ delta 去重语义不成立，停下报告，不得改用时间戳猜测。
- v15 迁移在真实旧库上触发意外重建/数据丢失迹象（free pages 异常暴涨、foreign_key_check 失败）→ 停下报告。
- replaceOrAppendBlock 对 FrameMigration 不生效（出现重复卡片堆叠）→ 先查 reducer.go:5137 路由 case 是否漏加，再查 Kind/StepID/RetainAsHistory 三条件，不得另起渲染旁路。
- 快路径与 orphan 检查/回滚语义出现冲突（快路径 skip 的 unit 在孤儿报告里误报）→ 停下报告。
- codex plan 名单回填发现 plan 名与 plan store 目录名不稳定（重跑名字漂移）→ 自愈判据不成立，停下报告。

## 11. Maintenance notes

- 权重分片（sessions 90%）是启发式；若未来某轻类目变重（如 codex sqlite 快照入账本），重算 `percentPlan` 分片即可，接口不变。
- 后续可选：`loadCodexState` 的 sqlite 快照按 (size,mtime) 账本跳过——同一张 fb_migration_units 表即可承载（target_id 用 `snapshot:<basename>` 命名空间）。
- 后续可选：三趟 discovery（picker 计数/preview/run）合并复用（claude plan 归属依赖 head 记录，合并时必须保留该数据）。
- 首迁超大记忆库（数千条笔记）的一次性 consolidation 仍是整库单趟（git diff 截断 + agent 循环）；若实际仍显慢，后续可在 importMemories 分批写+分批触发，属独立优化。
- 新增类目时必须同步 `percentPlan` 的分片表（阶段迁移状态机按 Stage 字符串开片/收片，新类目不登记则 bar 停在前一类目末尾）。
- 账本与 fb_sessions 行的手工不一致（用户手删行）由快路径的 sessionOwner 复核兜底：行没了 → 重新走全量导入。
- `memoryConsolidationRunner` 若将来增加非 workspace 输入（如依赖 stage1 行），`ConsolidateWorkspace` 的"仅增量"前提需要重新评估。

> 实施结果见同目录会话计划的 Implementation Status（2026-10-16 全部落地）：全套测试绿、tmux 场景 1-6 取证通过、改动留工作区未 commit。
