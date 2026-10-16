# /migrate 导入的会话在 /resume 面板不可见 — 根因修复计划（rev 2）

日期：2026-10-15
状态：已批准（plan-gated-bugfix 工作流），同日实施；实施记录见文末「验收记录」。
rev 2：吸收 deepseek-v4-flash 评审（三点全部采纳，逐条核实后修订；见 §9 评审处置记录）。

## 1. 目标（Why this matters）

用户在 TUI 里执行 `/migrate` 从 Claude Code 导入会话，迁移报告明确承诺：

```
Imported conversations — /resume lists them
    Deepseek-v4-flash 上下文压缩触发分析 · 416 messages · cli-2f2d88f2-0845-4d11-b435-352f315402ee
    ...
```

但随后执行 `/resume`，面板列表里**找不到任何迁移会话**。迁移承诺与实际行为直接矛盾；且不止列表不可见——显式 `forebrain resume <id>` 同样失败（见根因第 4 条）。本修复让迁移导入的会话真正可 resume，并修复已导入的存量数据（幂等重跑即回填）。

## 2. 根因（file:line 证据链，已逐一现场核实，含真实库只读复核）

**一句话：项目模型落地后，/resume 只列"当前项目"的会话，而 /migrate 的导入器从不给会话写 `project_id`，导入会话以 `project_id IS NULL` 落库，被所有项目作用域的读路径排除。**

1. **写入侧漏列**：`pkg/migrate/sessions.go` `insertSession` 的 INSERT 列集是
   `id, agent_id, title, updated_at, created_at, parent_session_id, cwd, git_branch, memory_mode, memory_source, initial_window_id, origin` —— **没有 `project_id`**。Claude 与 Codex 两个 source 共享这条路径（`importUnits` → `importOneSession` → `insertSession`），同漏。
2. **读侧按项目过滤**：TUI 启动即绑定项目（`pkg/tui/chat_session.go` `AdoptLaunchProject` 把启动目录注册为 fb_projects 行并设 `s.ProjectID`）。`/resume` 列表经 `pkg/tui/chat_surface.go`：`ProjectID` 非空时走 `ListSessionsForProject`；其 SQL（`pkg/state/session_store.go`）为
   `WHERE agent_id=? AND project_id=? AND source='' AND conversationHoldsAMessage` —— `project_id IS NULL` 的迁移会话被排除。
3. **agent_id 无 mismatch**（排除该假设）：`pkg/tui/chat_slash.go`，migrate `Options.AgentID` 取自 `s.sessStore().AgentID()`，与列表查询同一进程同一租户，必然一致。
4. **显式 resume 也被拒**：`pkg/tui/chat_session.go` `ResumeOnLaunchProject` 对会话查 `ProjectForSession`，`!ok` 即返回 `ErrResumeNoProject`（"conversation has no project"），TUI 启动路径把它显示为误导性的"技能工坊"提示（`cmd/forebrain/interactive.go` 一带）。即使面板列出也无法恢复。
5. **真实库复核（只读）**：`~/.forebrain/state/forebrain.state.sqlite` 中 `origin='migrated' AND project_id IS NULL` 恰好 **4 行**，id 与用户报告的 4 个会话完全一致；`agent_id='main'`；`cwd` 全部为 `/Users/doudou/workspace/unionj-cloud/forebrain-harness`（**非空**）——重跑回填路径可 100% 修复存量；该 root 的 project 行已存在（TUI 启动注册过），`FindOrCreateByRoot` 将命中同一行。
6. **既有语义先例（本修复归一对象）**：schema migration v9 `pkg/state/schema_migrations.go` `adoptProjectlessSessionsV9` 在项目模型落地时把全部无 project 普通会话按 **(agent, canonical cwd)** 绑定到项目：项目行不存在则创建（`findOrInsertProjectV9`，name=root 末段、project_key=ProjectKeyForRoot、created_at=最早会话），子会话继承父的项目与目录，cwd 为空的顶层会话被删除（`collectDoomedSessionsV9`，"projectless sessions with no working directory" 列为 doomed）。/migrate 导入器是后出的代码，完全绕过了这套语义。
7. **绑定 root 必须用 canonical root，不能用 memory.ProjectRoot**：`pkg/memory/scope.go` `ProjectRoot` 注释自述 "It is NOT a project identity… this walk-up survives only for the /migrate import matcher… Anything that needs 'which project is this' must use ProjectKey"。walk-up 宽匹配只用于 `--project` 的会话筛选（`pkg/migrate/sessions.go`、`codex_assets.go`）；归属判定用 canonical root。
8. **影响面（parity）**：TUI `/resume`（chat_surface.go）与 gateway web 的项目会话列表（`pkg/gateway/api_extra.go`，同一 `ListSessionsForProject`）都看不到迁移会话；依赖 `ProjectForSession` 的 resume/绑定路径（chat_session.go、gateway/server.go、process/run_executor.go、runner_pool.go、cron_service.go）对迁移会话全部失效。**写侧一处修复，两个 surface 同时恢复。**
9. **原生对照（证明正确写法长什么样）**：`pkg/state/session_store.go` `ensureSession` 出生即写 `project_id`（父 → 出生 → 进程默认三级回退），注释明确 "The project a session is born under never changes"。
10. **已排除的假设（rev 1 曾误判为"同族第二缺陷"，评审纠正）**：rev 1 曾提议给导入的 subagent 子会话写 `source='subagent'`。核实后**撤回**：
    - `state.SessionSourceSubagent` 是 `pkg/run/subagent.go` 运行时 worker 会话的出生标记；
    - 全库唯一消费它的查询全部是**排除**它（`session_store.go` `ListChildSessionsRecent`、`ListSessionsRecent`、`ListSessionsRecentPaged`），没有任何列表会显示 source='subagent' 的会话——写入该值会让迁移子会话从 /resume 与子会话列表**全部消失**，只能按 id 直达；
    - 迁移设计文档是相反契约：`docs/plan/CLAUDE_CODE_MIGRATION_PLAN.md` §4.4「子代理会话本身可 resume、可读」、验收表「每个 `subagents/agent-*.jsonl` 都有一个 child session 可 resume 查看」；报告 `plan.go` 也用 `↳` 缩进把子会话渲染在 "Imported conversations — /resume lists them" 之下。
    - 真实库复核：43 个迁移子会话中 40 个已满足"source='' 且持有消息"的列出条件——保持 source='' 即恢复其可见性。
    - **结论：导入子会话保持 source=''（与主会话同为普通会话），本修复不碰 source 列。**

## 3. 修复设计

全部改动集中在 **pkg/migrate 写入侧**；读路径（ListSessionsForProject / ResumeOnLaunchProject / web 列表）语义正确，一律不动。**不写 `source` 列**（保留默认 ''，见 §2.10）。

### 3.1 绑定 root 解析（新语义函数，sessions.go）

```go
// importProjectRoot resolves the root of the project an imported conversation
// binds to: the launch project's root when the import was scoped to it, else
// the conversation's own working directory. Empty means the source recorded no
// directory and the conversation cannot belong to a project.
func importProjectRoot(opts *Options, cwd string) string {
    if opts.OnlyProject {
        return strings.TrimSpace(opts.CurrentProjectRoot)
    }
    return strings.TrimSpace(cwd)
}
```

- **OnlyProject 模式**（TUI scope 选 "Current project only"，`pkg/tui/commands.go`）：matcher 已用 `memory.ProjectRoot` 宽匹配把子目录 cwd 的会话算作"属于本项目"（sessions.go），因此绑定必须落在 `CurrentProjectRoot`，否则子目录会话绑去子目录项目、在本项目 /resume 依然不可见——与该模式的用户意图矛盾。
- **All projects 模式**（TUI 默认选中项）：每会话按自己的 cwd 归属（v9 语义）。**已知并接受的行为**（v9 已确立的先例）：源历史里出现的每个不同目录都会得到一个 project 行（name=目录末段），项目列表因此会出现这些目录——这是历史会话归属各自目录的直接后果，不是缺陷。
- root 交由 `state.NewProjectStore(opts.DB, opts.AgentID).FindOrCreateByRoot(ctx, root, state.CreateProjectInput{ResourceAccess: true})` canonical 化并幂等取行（`pkg/state/project_store.go`）。`FindOrCreateByRoot` 走 `canonicalProjectRoot`（`canonicalStatePath`，`EvalSymlinks` 失败回退 `Clean(abs)`，**不要求目录存在**——老项目目录已删除也能绑定）；参数对齐 TUI 的 `AdoptLaunchProject`（ResourceAccess: true；MemoryScope 空值经 `NormalizeProjectMemoryScope` 归一为 "shared"，与 v9 插入值一致）。OnlyProject 模式下 TUI 已注册过该 root，FindOrCreateByRoot 拿到同一行。
- **dry-run 绝不调用 FindOrCreateByRoot**（它无条件 INSERT 缺失行）：dry-run 只做 root 字符串判定与 skip/rebound 计数，不触碰库（`TestRunClaudeDryRunWritesNothing` 的契约，扩展断言 fb_projects 行数不变）。
- **pkg/migrate 已 import pkg/state**，无层天花板问题。

### 3.2 insertSession 补 project_id（不碰 source）

- 新增参数 `projectID string`；INSERT 列集只追加 `project_id`。
- `cwd` 列改写 `project.Root`（canonical root），对齐 v9 的 `SET project_id=?, cwd=?`——同一目录在库里只有一种拼写。
- 子会话与父同 cwd（见 §3.3 的兜底后依然成立：claudeUnits 子单元显式继承主会话 Cwd；codexUnits 子继承父），两种模式都解析到与父相同的 project 行，天然"子随父"。

### 3.3 空 cwd：项目级路径兜底后仍空才拒绝（评审 rev 2 采纳：先兜底、最小拒绝面）

- **claudeUnits**：构造 parent unit 时先 `resolved := resolveProjectPath(project, data.JSONProjectPaths)`（B5 第一权威：~/.claude.json projects map → 会话 cwd → 文件系统探测），`unit.Cwd = firstNonEmpty(session.Cwd, resolved)`——**两种模式都做**（目前只在 OnlyProject 分支解析）。会话级 cwd 优先，项目级路径兜底。对齐 B5 验收精神（"没有任何来源目录因为解析不出项目路径而被丢弃"）。
- **codexUnits**（codex_assets.go）：Codex 无 encoded-project 目录结构，Cwd 已取 `firstNonEmptyStr(meta.Cwd, rollout.Cwd)`，无项目级兜底源，维持现状。
- 兜底后 `importProjectRoot` 仍返回空 → `SessionOutcome{Status: StatusSkipped, Reason: "no working directory: a conversation must belong to a project"}`，不落库。依据：v9 把"无目录的无项目会话"列为 doomed 删除——项目模型下普通会话必须有目录；写 `project_id IS NULL` 正是本 bug 的落库状态，不允许再造。
- OnlyProject 模式下 root 恒为 CurrentProjectRoot，"空 cwd"不构成拒绝（matcher 已按项目过滤）。

### 3.4 存量回填（幂等重跑修复，覆盖用户当前已导入的 4 个会话）

- `sessionOwner` 查询扩展为 `SELECT agent_id, origin, project_id FROM fb_sessions WHERE id=?`，多返回 projectID。
- `sessionOutcomeFor` 的 `already migrated` 分支：`projectID` 非空 → 维持现状 skip；为空 → outcome.Status 置新常量 `StatusRebound = "rebound"` 并标记需回填。
- `importOneSession`：`!pending` 且 `outcome.Status == StatusRebound` 时执行 `rebindMigrated`：
  1. root = `importProjectRoot(opts, parsed.Cwd)`；空 → 维持 skipped，Reason="already migrated without a working directory; nothing to bind"（库中该行保持原状，报告中可见原因，**不允许静默 NULL**）。
  2. dry-run：不写库，直接返回 StatusRebound（Reason="would bind to its project"）——dry run 与 run 对 scope 判定一致。
  3. 真跑：事务外 `FindOrCreateByRoot`（幂等）→ 该会话独立事务执行
     ```sql
     UPDATE fb_sessions
     SET project_id=?, cwd=?
     WHERE id=? AND agent_id=? AND origin='migrated'
     ```
     **不动 updated_at、不动 source**（/resume 按 updated_at 排序，回填不得改变时间序；source 语义见 §2.10）。`AND origin='migrated'` 是硬边界：绝不触碰原生会话行。
- 幂等性保持：重跑时已绑定的迁移会话仍走 `already migrated` skip（`TestRunClaudeImportsEverythingAndIsIdempotent` 的既有语义不破）。

### 3.5 报告（pkg/migrate/plan.go）

- **不改 `SessionCounts` 签名**（4 返回值被 `parse_test.go`、`codex_test.go` 等按位置消费）。rebound 计数在 `Text()` 内用 `countStatus(r.Sessions, StatusRebound)` 单独统计。
- 汇总行追加：`Conversations: N imported · N rebound · N already here · N failed · N belong to another agent`（rebound 为 0 时省略该段，保持既有输出的字节稳定性）。
- `migrated > 0` 小节之后，`rebound > 0` 时新增小节 `Rebound conversations — imported earlier, now bound to their projects`，逐行 `Title · TargetID`（复用既有渲染形状，IsChild 缩进同款）。

### 3.6 行为变化矩阵

| 场景 | 旧行为 | 新行为 | 备注 |
|---|---|---|---|
| /migrate 导入有 cwd 的主会话（All 模式） | project_id NULL；/resume 不可见、resume 被拒 | 绑定 (agent, canonical cwd) 项目；/resume（在对应目录启动时）可见、可恢复 | 修复主诉求 |
| /migrate 导入（Current project only 模式） | 同上不可见 | 绑定 CurrentProjectRoot 的项目（含子目录 cwd 会话） | 与 matcher 宽匹配语义对齐 |
| /migrate 导入 subagent 子会话 | source=''，project_id NULL，不可见 | source='' **不变**，继承父项目；随主会话一起入 /resume | 迁移设计文档 §4.4"可 resume、可读"契约（§2.10） |
| 会话 cwd 空但项目目录可解析（claude） | 落库但不可见 | Cwd 兜底项目路径，正常绑定 | rev 2：B5 兜底 |
| 兜底后仍无任何目录 | 落库但不可见（bug 状态） | 拒绝导入，skipped + reason；重跑存量同类行保持原状并在报告给出原因 | 对齐 v9 doomed 语义，无静默 NULL |
| 重跑 /migrate（已迁移且已绑定） | skipped "already migrated" | 不变 | 幂等保持 |
| 重跑 /migrate（已迁移但无绑定，即存量 bug 数据） | skipped "already migrated"，永远不可见 | StatusRebound：回填 project_id/cwd，随后 /resume 可见 | 用户当前 4 个会话的修复路径（cwd 已核实非空） |
| 原生会话 | 不受影响 | 不受影响（UPDATE 限定 origin='migrated'） | 硬边界 |
| dry-run | 不写库 | 不写库（含 fb_projects：不调用 FindOrCreateByRoot） | 显式断言 |
| web 项目会话列表 | 看不到迁移会话 | 写侧修复后自动可见 | 无需改 gateway |

## 4. Scope

**In scope**
- `pkg/migrate/sessions.go`（insertSession、importOneSession、sessionOwner、sessionOutcomeFor、importProjectRoot、rebindMigrated、claudeUnits 兜底、StatusRebound 常量）
- `pkg/migrate/plan.go`（报告：Text() rebound 汇总与小节；SessionCounts 签名不动）
- `pkg/migrate/sessions_test.go`（新增 4 个测试 + 既有断言随契约更新）
- `pkg/migrate/plan_test.go`（若报告文本有既有断言，随契约更新）
- `docs/plan/MIGRATE_RESUME_PROJECT_BINDING_PLAN.md`（本计划落盘）与 `docs/plan/CLAUDE_CODE_MIGRATION_PLAN.md`、`docs/plan/CODEX_MIGRATION_PLAN.md`（设计文档补记"会话绑定项目"一节，两文档同补）

**Out of scope（看起来相关但明确不碰）**
- `pkg/state/session_store.go`、`pkg/state/project_store.go`：读路径与 FindOrCreateByRoot 语义正确。
- `pkg/tui/chat_surface.go`、`pkg/tui/chat_session.go`：/resume 过滤与 ResumeOnLaunchProject 是正确产品语义（项目隔离）。
- `pkg/gateway/*`：写侧修复后自动恢复，不动。
- `pkg/migrate/parse.go`、`claude.go`、`codex_assets.go` 的解析逻辑（codexUnits 不动；claudeUnits 只加 Cwd 兜底一行级改动）。
- `pkg/migrate/parse_test.go`、`pkg/migrate/codex_test.go`：不改 SessionCounts 签名即不受影响，明确不动。
- `fb_sessions.source` 列与一切 source 语义（§2.10 裁决）。
- schema migration（pkg/state/schema_migrations.go）：不新增 v 迁移——回填由 /migrate 幂等重跑承担，不经 schema 层。

## 5. Steps（每步带验证）

1. **落盘计划**：本文件复制为 `docs/plan/MIGRATE_RESUME_PROJECT_BINDING_PLAN.md`。
2. **sessions.go 绑定与写入**：实现 3.1–3.3（importProjectRoot、claudeUnits Cwd 兜底、insertSession 补 project_id、空 cwd 拒绝）。导入路径在 `importOneSession` 事务开启前解析 project（FindOrCreateByRoot 用 opts.DB，不在 tx 内）；dry-run 只做字符串判定，不建任何行。
3. **sessions.go 存量回填**：实现 3.4（sessionOwner 扩列、StatusRebound、rebindMigrated）。
4. **plan.go 报告**：实现 3.5（countStatus 单独计数，签名不动）。
5. **测试**（fixture 沿用 `state.OpenStateForTest` + 既有 claude fixture）：
   - `TestImportBindsConversationsToAProject`：All 模式导入（cwd=临时目录）→ 断言 fb_sessions.project_id 等于 `FindOrCreateByRoot(同 cwd)` 的 id、cwd=canonical root；断言 `state.NewSessionStore(db, agent).ListSessionsForProject` 列出该主会话与其 subagent 子会话（真实 /resume 查询路径 + §2.10 可读契约）。
   - `TestImportScopedToProjectBindsToItsRoot`：OnlyProject=true、CurrentProjectRoot=临时项目根、会话 cwd=项目根下子目录 → 绑定到项目根的 project。
   - `TestImportFallsBackToTheSourceProjectPathAndSkipsOnlyWithoutAny`：会话头无 cwd 但 ~/.claude.json projects map 可解析 → 兜底绑定成功；项目级也解析不出 → skipped + reason，库中无行。
   - `TestReimportRebindsProjectlessMigratedConversations`：正常导入后手工 `UPDATE fb_sessions SET project_id=NULL`（模拟旧版落库）→ 重跑 → StatusRebound、project_id 回填、cwd=canonical root、updated_at 不变、source 不变、原生行（另造一行非 migrated）不动；第三次跑 → 全 skipped（幂等）。
   - `TestRunClaudeDryRunWritesNothing` 扩展：dry-run 后断言 `fb_projects` 行数不变。
   - Sweep 既有测试：`TestRunClaudeImportsEverythingAndIsIdempotent`、`TestImportSessionOwnedByAnotherAgent`、`TestImportSubagentSession`、`TestRunClaudeDryRunWritesNothing` 及 plan_test.go 的报告文本断言，随契约更新并注明理由。（`TestImportSubagentWithoutAnImportedParent` 断言仅涉 parent/owner，**无需更新**。）
6. **编译与定点测试**：
   ```bash
   CGO_ENABLED=1 go build ./...
   CGO_ENABLED=1 go test -tags fts5 ./pkg/migrate -count=1
   CGO_ENABLED=1 go test -tags fts5 ./pkg/state ./pkg/tui -count=1   # 读路径回归
   go vet ./...
   ```
7. **文档补记**：CLAUDE_CODE_MIGRATION_PLAN.md / CODEX_MIGRATION_PLAN.md 各补"Session project binding"小节（v9 语义归一、OnlyProject 绑定根、Cwd 项目级兜底、空目录拒绝、rebound 回填、source 保持 ''）。
8. **tmux 真机验收**（构建必须 `-tags fts5`；临时 FOREBRAIN_HOME + 临时项目目录 + 伪造 ~/.claude 源，绝不碰真实 ~/.forebrain*）：
   - 场景 1 复现→修复：伪造 claude home（1 主会话 + 1 subagent transcript，head 带 cwd=临时项目目录）→ TUI 跑 /migrate（All projects）→ /resume 面板列出导入主会话与子会话 → Enter 恢复成功（不再 ErrResumeNoProject、不再出现"技能工坊"误导文案）。
   - 场景 2 存量回填：导入后 `sqlite3` 手工置 project_id=NULL → 重跑 /migrate → 报告含 rebound → /resume 可见。
   - 场景 3 回归：原生新会话照常创建、/resume 照常列出；OnlyProject 模式子目录 cwd 会话也进当前项目。
   - 场景 4 dry-run：/migrate claude --dry-run 不写库（fb_sessions/fb_projects 行数均不变，scope 判定与真跑一致）。
   - 逐屏 `tmux capture-pane -p` 取证。

## 6. Done criteria（机器可查）

1. `CGO_ENABLED=1 go test -tags fts5 ./pkg/migrate ./pkg/state ./pkg/tui -count=1` 全绿；`go vet ./...` 干净。
2. 新测试断言 `ListSessionsForProject`（/resume 真实查询）列出导入主会话与 subagent 子会话。
3. 重跑 /migrate 后，真实库中**不存在静默 NULL**：`SELECT id FROM fb_sessions WHERE origin='migrated' AND project_id IS NULL` 的每一行都在本次报告中有明确 skipped 原因（no working directory）；无原因的 NULL 行数（即应回填而未回填）为 0。当前真实库 4 行 NULL 的 cwd 全部非空（已核实），重跑后应为 0 行。（真实库的重跑由 owner 在验收后手动执行；实施与验收过程绝不连接真实 `~/.forebrain`。）（059 收尾修订：盘点限于源文件仍存在的会话；源已删除的行无法回填，由报告以 "source transcript no longer present" outcome 点名，不计入"应回填而未回填"。）
4. 真实库 4 个存量 id（cli-2f2d88f2…、cli-62e9a73d…、cli-925ebacb…、cli-e9871abe…）在回填后 `ResumeOnLaunchProject` 返回 nil（等价验收：临时环境中同构数据 Enter 恢复成功；真实库由 owner 重跑后确认 /resume 直接可见）。
5. dry-run 前后 fb_sessions 与 fb_projects 行数不变（测试断言）。
6. tmux 验收四场景逐屏取证通过。

## 7. STOP conditions

- 若实施中发现 `fb_projects` 在 (agent_id, root) 上没有唯一约束（FindOrCreateByRoot 的 `ON CONFLICT(agent_id, root) DO NOTHING` 依赖它）或表结构与 project_store.go 现状不符 → STOP 报告，不自行改 schema。
- 若发现 /resume 面板还有 project_id 之外的过滤导致导入会话仍不可见 → STOP 报告证据链。
- 若既有测试揭示 `insertSession` 的列集被 replay/compaction 路径以位置敏感方式依赖（新增列破坏读取）→ STOP 报告，不硬改。
- 实施与验收绝不连接真实 `~/.forebrain`；真实库回填只经 owner 手动重跑 /migrate 完成。

## 8. Maintenance notes

- 未来给 fb_sessions 增加新的"出生即定"列（类似 project_id）时，`pkg/migrate/insertSession` 与 `pkg/state/ensureSession` 必须同步——这次 bug 的本质是两处出生写入分叉。
- v9 的 `adoptProjectlessSessionsV9` 与本次修复是同一语义的两个实例（schema 迁移版 / 导入器版）；后续若再出现"会话落库"路径（如新的导入源），绑定规则 = canonical root（OnlyProject 则 CurrentProjectRoot）+ Cwd 项目级兜底 + 子随父 + 兜底后仍无目录才拒绝。
- `fb_sessions.source` 对"迁移的历史子会话"与"运行时 subagent worker"是两种身份：前者 source=''（可读可 resume，迁移设计文档 §4.4 契约），后者 source='subagent'（worker，不入列表）。不要把后者语义套到前者。
- 报告新增 StatusRebound 后，任何按 Status 字符串聚合的消费方（plan.go 渲染、未来 web 迁移页）需把它计入"已处理"而非"失败"。
- All-projects 模式会为源历史里每个不同 cwd 目录创建 project 行（v9 先例）：项目列表增长是预期行为，review 时不要当缺陷清掉。

## 9. 评审处置记录（rev 2）

deepseek-v4-flash 评审三点全部采纳，逐条独立核实：

1. **撤 source='subagent' 写入**（原 §3.2/§3.4）：核实属实——subagent.go worker 专用出生标记；session_store.go 各列表查询全部排除该值，无任何列出方；迁移设计文档 §4.4 与验收表、报告 ↳ 渲染、真实库 43 个迁移子会话中 40 个已满足列出条件（评审称 38，复核条件略有出入、结论一致）——写入该值会使迁移子会话完全不可达，违反契约。已从设计、矩阵、测试、Scope 全面移除，并落为维护注记。
2. **空 cwd 先兜底再拒绝**（原 §3.3）：采纳——claudeUnits 增加 resolveProjectPath 项目级兜底（B5 第一权威，两种模式都做），兜底后仍空才拒绝；Done criterion 3 相应改为"无静默 NULL"。
3. **SessionCounts 签名不动**（原 §3.5）：采纳——rebound 在 Text() 内 countStatus 单独统计；parse_test.go/codex_test.go 明确移出影响面（Out of scope 显式列出）。
4. 评审改进点同样采纳：`TestImportSubagentWithoutAnImportedParent` 移出 sweep 清单（断言仅 parent/owner）；新增 Done criterion 4（ResumeOnLaunchProject 对 4 个存量 id 返回 nil / Enter 恢复成功）；dry-run 不建 fb_projects 行显式化（§3.1 + 测试断言）。
5. 评审开放问题处置：子会话可见性按迁移设计文档裁决为"可见"（§2.10，本计划不再引入相反变更）；All 模式项目行增长 acknowledge 进 §3.1 与维护注记。

## 验收记录（实施后回填）

实施日期：2026-10-16（同日完成代码、测试、文档与 tmux 真机验收）。

### 单元与编译

- `CGO_ENABLED=1 go test -tags fts5 ./pkg/migrate -count=1` 全绿（含 4 个新测试：TestImportBindsConversationsToAProject、TestImportScopedToProjectBindsToItsRoot、TestImportFallsBackToTheSourceProjectPathAndSkipsOnlyWithoutAny、TestReimportRebindsProjectlessMigratedConversations；TestRunClaudeDryRunWritesNothing 扩展 fb_projects 断言）。
- `CGO_ENABLED=1 go test -tags fts5 ./pkg/state ./pkg/tui -count=1` 全绿；`go build ./...`、`go vet ./...`、`gofmt` 干净。
- STOP 条件核对：`fb_projects` 在 (agent_id, root) 上确有唯一索引 `idx_fb_projects_agent_root_unique`（v9 迁移创建，schema_migrations_test.go 断言），FindOrCreateByRoot 的 ON CONFLICT 依赖成立；insertSession 列集无位置敏感消费方（唯一调用方按列名读取）。

### 实施中发现的计划外事实（已按根因修掉）

- claudeUnits 子单元原取 `session.Cwd` 原始值；Cwd 兜底落在父单元后，子单元必须继承父单元兜底后的 Cwd（`Cwd: parent.Cwd`），否则空 cwd 会话的子代理在兜底场景被"no working directory"拒绝。测试首跑暴露，随本修复一并落地。

### tmux 真机验收（临时 FOREBRAIN_HOME=/tmp/fb-migrate-acc/home + 伪造 claude home，未触碰真实 ~/.forebrain*）

- 场景 4（dry-run）：picker 流的确认前预览即 PlanSource（DryRun=true）。预览算出"1 conversation + 1 subagent · 12 messages"，Esc 取消前后 fb_sessions/fb_messages 均为 0 行；fb_projects 的 1 行经重启取证确认为 TUI 启动注册的 launch project（dry-run 之前已存在），dry-run 零写入。（注：TUI 的 /migrate `SupportsInlineArgs:false`，`--dry-run` inline 形态不存在，预览取消即 dry-run 等价物。）
- 场景 1（复现→修复）：伪造源（1 主会话 + 1 subagent，head cwd=临时项目目录）经 /migrate（All projects）导入，报告"2 imported · Imported conversations — /resume lists them"；/resume 面板列出 `My Fixed Title` 与 `↳ find the root cause`；Enter 恢复成功——transcript 完整回放（含 compact 边界与 "resumed 10 messages above"），无 ErrResumeNoProject、无"技能工坊"误导文案；DB 三行 project_id 均指向 launch project，cwd 均为 canonical root（/private/tmp/...）。
- 场景 2（存量回填）：`UPDATE fb_sessions SET project_id=NULL, cwd='/tmp/old-spelling' WHERE origin='migrated'` 后重跑 /migrate → 报告"0 imported · **2 rebound**"+ "Rebound conversations — imported earlier, now bound to their projects" 小节；DB 回填 project_id/cwd=canonical，source 仍 ''；子会话 updated_at 原样（主会话 updated_at 的变化来自"Migration complete"完成卡按既有归属写入当前会话——tmux 屏幕可见该卡片，rebind UPDATE 本身不动 updated_at，单元测试已钉死）；/resume 重新可见。
- 场景 3（回归 + OnlyProject）：重启后原生会话正常创建/绑定/续聊，/resume 同列原生与导入会话；PROJECT/.git + cwd=$PROJ/deep/sub 的会话经 "Current project only" 导入，绑定到**项目根**的同一 project 行（cwd 归一 canonical root），/resume 列出。
- 验收中踩的坑：OnlyProject 子目录场景必须把子目录真实落盘——`memory.ProjectRoot` 对不存在的路径不做 EvalSymlinks，/var 与 /private/var 两种拼写不等导致 matcher 过滤（单测同因，已修）。

### 遗留

- 真实库 `~/.forebrain/state/forebrain.state.sqlite` 的 4 个存量 NULL 会话：由 owner 验收后在真实环境重跑 `/migrate`（All projects）回填，预期报告"4 rebound"；重跑后 `SELECT id FROM fb_sessions WHERE origin='migrated' AND project_id IS NULL` 应为 0 行。
- 工作区另有 EMPTY_SESSION_FILTER 计划的既有未提交改动（pkg/state、pkg/gateway、pkg/tui、frontend 等 9 文件），与本修复无关，未触碰。
