# 059：migrate 项目绑定与空会话过滤的收尾（审计 #9/#10/#20）

> **执行者须知（零上下文）**：本计划自包含。逐步执行，每步先跑"验证"确认预期再进入下一步。
> 命中 STOP condition 就停下报告。**不建 worktree、不 commit**。
>
> **Drift check（先做）**：本计划写于 `git rev-parse --short HEAD` = `fa4af8a`（2026-10-16），
> **且建立在当时工作区尚未提交的 migrate/state 改动之上**（EMPTY_SESSION_FILTER 与
> MIGRATE_RESUME_PROJECT_BINDING 两计划已实施、未提交——`git status --porcelain` 可见
> pkg/migrate/{plan,sessions,sessions_test}.go、pkg/state/session_store*.go 等改动）。
> 本计划的所有 "Current state" 摘录来自**工作区当前状态**（不是 HEAD）。执行前若工作区 migrate/state
> 文件与摘录对不上（例如 owner 已提交或改动被重写），逐条重核 file:line，对不上按 STOP 处理。

## Status

- **Priority**: P2（两条正确性尾巴 + 一组测试钉；无安全/数据风险）
- **Effort**: S/M
- **Risk**: LOW-MED（子会话绑定语义改动会影响迁移结果，需按 v9 先例严格对齐）
- **Depends on**: 工作区里 MIGRATE_RESUME_PROJECT_BINDING 的实施（已完成、未提交）
- **Category**: Correctness / Tests（审计发现 #9/#10/#20）
- **Planned at**: commit `fa4af8a` + 未提交工作区，2026-10-16

## Why this matters

上一轮实施（EMPTY_SESSION_FILTER + MIGRATE_RESUME_PROJECT_BINDING）已把主链路修通，分支审计
复核后确认四处收尾：

1. **迁移子会话可绑离父项目**（#9）：子单元带 `Cwd: parent.Cwd`，但 `applyDefaults` 只在子自身
   cwd 为空时回退——子 transcript 记录了自己的 cwd（Claude 的 .meta/transcript 有 cwd 记录、
   Codex rollout 同）时，All-projects 模式下绑去**子 cwd 的项目**，与代码注释（"binds to the
   same project"）、计划 §3.2"子随父"、schema v9 先例（"子会话继承父的项目与目录"）全部相悖；
   而迁移报告把子会话用 `↳` 印在父之下，承诺它在 /resume 可见。
2. **DeletePreview 计数与新"会话"定义脱节**（#10）：项目删除确认框的"将同时删除 N 个对话"计数
   无 `conversationHoldsAMessage`、无 source 过滤，注释还声称 "matches the list the user sees"——
   列表显示 2 条、确认框说删 5 条，注释误导后来者。
3. **codex `OnlyProject` + 空 root 静默关掉过滤器**（#20a）：`codex_assets.go:66` 的守卫
   `opts.OnlyProject && CurrentProjectRoot != ""` 在 root 为空时放行全部单元，随后每个单元被
   `importProjectRoot` 判"no working directory"跳过——误诊为"会话没有目录"；claude 侧同配置
   则全量过滤。同一误配置两个 source 行为相反。
4. **回填是发现绑定的 + dry-run 无钉**（#20b/#20c）：源 transcript 已删除的历史迁移行永远
   project_id NULL 且报告无一行提及（与计划 Done 判据 3"无原因的 NULL 行为 0"冲突）；
   dry-run 对"已存在 NULL 行"的只读性没有测试钉住。

## Current state（摘自工作区当前代码，执行前重核）

### 子会话绑定（pkg/migrate/sessions.go）

```go
// :67-72 applyDefaults —— 只在空时回退，子自己的 cwd 优先
func (u sessionUnit) applyDefaults(parsed *parsedSession) {
	...
	if strings.TrimSpace(parsed.Cwd) == "" {
		parsed.Cwd = u.Cwd
	}
// :195-210 claudeUnits 子单元 —— 注释承诺与实现不符
			units = append(units, sessionUnit{
				...
				ParentID: parent.TargetID,
				IsChild:  true,
				// The child inherits the parent's effective cwd,
				// project-level fallback included: it ran where its
				// parent ran, so it binds to the same project.
				Cwd: parent.Cwd,
			})
// :316-325 importProjectRoot（OnlyProject 模式 root=CurrentProjectRoot，与 cwd 无关）
// :335-349 root == "" → StatusSkipped "no working directory: ..."
```

Codex 侧同构：`pkg/migrate/codex_assets.go:60-64` `if strings.TrimSpace(child.Cwd) == "" { child.Cwd = parent.Cwd }`
+ `:66` `if opts.OnlyProject && strings.TrimSpace(opts.CurrentProjectRoot) != "" { …过滤… }`。

`parse.go:332-333`：parsed.Cwd 来自 transcript 自身的 cwd 记录（后写胜）——这是"子优先"的来源。

### rebind 路径（pkg/migrate/sessions.go:106-153，工作区版）

DryRun 分支：`sessionOutcomeFor` + `importProjectRoot==""` 时置 Skipped；`pending` 时记
StatusMigrated 计数——**不触库**。真跑分支：`importOneSession` → `StatusRebound` 时
`rebindMigrated`。rebind 只覆盖**本轮发现**的单元（claudeUnits/codexUnits 产出）。

### DeletePreview（pkg/state/project_store.go:563-576，工作区版）

```go
// DeletePreview reports what Delete would remove: the project's own
// conversations (sub-sessions excluded, so the count matches the list the
// user sees), its cron jobs, and whether a run is in flight.
if err = s.db.QueryRowContext(ctx,
	`SELECT COUNT(*) FROM fb_sessions WHERE project_id=? AND agent_id=? AND parent_session_id IS NULL`,
	id, s.agentID).Scan(&sessions); err != nil { ... }
```

无 `conversationHoldsAMessage`、无 source 过滤。消费方：`frontend/src/views/ProjectsView.vue:141-148`
+ `frontend/src/locales/index.ts:668-669`（"将同时删除 {sessions} 个对话"）。

### 既有测试锚点

`pkg/migrate/sessions_test.go`：`TestReimportRebindsProjectlessMigratedConversations`（真跑
rebind）、`:205-213` 附近的 dry-run 断言（fresh DB、无 rebind 候选）；`TestImportBindsConversationsToAProject`
（含子会话可见性钉）。

## 修复设计

### Step 1（#9）：子随父——IsChild 单元的绑定以父为准

v9 语义（`schema_migrations.go` adoptProjectlessSessionsV9：子会话继承父的项目与目录）落到
导入器：

1. `pkg/migrate/sessions.go` `applyDefaults`：为 `u.IsChild` 增加**权威覆盖**（不再只回退）：

```go
if u.IsChild && strings.TrimSpace(u.Cwd) != "" {
	// A subagent conversation belongs to its parent's project (the v9
	// precedent: children inherit the parent's project and directory). Its
	// own transcript may record a cwd it ran a subtask in; binding there
	// would split it from the parent the report prints it under.
	parsed.Cwd = strings.TrimSpace(u.Cwd)
}
```

   （放在现有空值回退之前；`u.Cwd` 为空时维持现状——父也无目录的情形本就 Skipped。）
2. Codex 侧对齐：`codex_assets.go:60-64` 的 `if child.Cwd == ""` 改为同语义的权威覆盖
   （`child.IsChild` 已在 :62 附近设置）。
3. **OnlyProject 模式不受影响**：`importProjectRoot` 在该模式返回 `CurrentProjectRoot`，与
   cwd 无关——Step 1 只改 All-projects 模式的行为。
4. 修正 claudeUnits :201-206 的注释使其与实现一致（或保留——实现改后注释已成立）。
5. **rebind 路径同规则**：核对 `rebindMigrated`/`sessionOutcomeFor` 对 child 单元用的 root 与
   import 一致（同一 `importProjectRoot(opts, parsed.Cwd)`，Step 1 已把 parsed.Cwd 归一到父）。

**验证**：T1。

### Step 2（#10）：DeletePreview 计数对齐"用户看得见的对话"

决策（本计划推荐，owner 评审可否）：**计数采用与列表同族的谓词**——空壳行在会话页本就不可见
（EMPTY_SESSION_FILTER 语义），确认框按"可见对话"计数；被顺带删除的不可见空壳行不计数
（它们本来就不构成用户心智里的"对话"）。实现：

```go
if err = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM fb_sessions
	WHERE project_id=? AND agent_id=? AND parent_session_id IS NULL
	  AND conversationHoldsAMessage`, id, s.agentID).Scan(&sessions); err != nil { ... }
```

（`conversationHoldsAMessage` 是 session_store.go 已有的 SQL 片段常量——工作区版已引入，直接拼接；
若它带 `source=''` 过滤则一并生效，保持与 ListSessionsForProject 同族。执行时核对该常量的完整
定义，使计数与列表谓词完全一致。）注释同步改写为"counts the conversations the sessions tab
lists (empty shells are deleted too but not counted)"。

前端文案不动（"对话"语义因此变真）。若 owner 想让确认框如实报告全部将被删除的行数，
改由 owner 在评审时提出——本计划按推荐方案落地。

**验证**：T2。

### Step 3（#20a）：OnlyProject + 空 root 在入口统一报错

在 `Options` 消费的单一咽喉（`pkg/migrate/plan.go` 的 Run 入口，执行时定位 `opts.OnlyProject`
首次消费处）加校验：

```go
if opts.OnlyProject && strings.TrimSpace(opts.CurrentProjectRoot) == "" {
	return nil, errors.New("current project root is empty: scoping the import to the current project requires a project root")
}
```

codex_assets.go:66 的守卫保留（双保险，不再是唯一防线）。claude/codex 两个 source 因此对称：
同配置直接失败并说清原因，而不是各自静默。

**验证**：T3。

### Step 4（#20b）：回填盲区写进报告

Run（或 rebind 汇总处）末尾对库做一次只读盘点：

```sql
SELECT id, cwd FROM fb_sessions
WHERE origin='migrated' AND agent_id=? AND project_id IS NULL
  AND id NOT IN (本轮任何 outcome 涉及的 target id)
```

对每行追加一条 `SessionOutcome{TargetID: id, Status: StatusSkipped, Reason: "source transcript no
longer present; cannot resolve its project — it stays unlisted in project-scoped /resume"}`（新增
只读 outcome，不写库）。同时在 `docs/plan/MIGRATE_RESUME_PROJECT_BINDING_PLAN.md` 的 Done 判据 3
补一句口径修订："盘点限于源文件仍存在的会话；源已删除的行在报告中点名"。（该文档是已实施计划
的记录，只补这一句，不动其它内容。）

**验证**：T4。

### Step 5（#20c）：dry-run 只读性钉住 rebind 候选

扩展 `TestReimportRebindsProjectlessMigratedConversations`：先真跑一轮 → 手工把那几行
`project_id` 置 NULL（模拟历史遗留）→ 以 `DryRun=true` 重跑 → 断言 (a) outcome 为
StatusRebound/"would bind"语义（与现有 outcome 文案一致）；(b) `fb_sessions`/`fb_projects`
两表内容与 dry-run 前**逐字节一致**（快照比对行集合）。

**验证**：T5（即本步骤产出）。

## Test plan

- **T1**（Step 1）`pkg/migrate/sessions_test.go`：fixture——父 transcript cwd=/proj-a，子
  transcript 自带 cwd=/sub-elsewhere → All-projects 导入后父与子的 `project_id` 同指
  /proj-a 的项目行；OnlyProject 模式下同 fixture 仍绑 CurrentProjectRoot（不回归）。
  Claude 与 Codex 各一例（Codex 用现有 codex fixture 构造方式）。
- **T2**（Step 2）`pkg/state/project_store_test.go`：一个项目下 2 条有消息会话 + 3 条空壳 +
  1 条 cron → `DeletePreview` 返回 sessions==2（谓词若含 source 过滤则 cron 也不计）。
- **T3**（Step 3）`pkg/migrate/plan_test.go`（或 Run 所在文件的测试）：OnlyProject+空 root →
  Run 返回错误且文案点名；claude/codex 两 source 同断言。
- **T4**（Step 4）`pkg/migrate/sessions_test.go`：导入两轮后删除一条源 .jsonl 再重跑 → 报告
  含该 id 的 "source transcript no longer present" outcome；库中该行 project_id 仍 NULL、
  无新写。
- **T5**（Step 5）dry-run 快照逐字节一致断言。

## STOP conditions

1. 工作区的 migrate/state 改动与 "Current state" 摘录不符（被重写/已提交且行号漂移）——逐条
   重核后仍对不上即停。
2. `conversationHoldsAMessage` 常量的实际定义与"列表谓词"不一致（比如列表还叠加了别的条件）
   ——以列表真实谓词为准调整计数 SQL，两处不一致时停下报告差异。
3. Step 1 的权威覆盖导致既有测试中"子会话按自身 cwd 绑定"的钉（若有）变红——那类测试钉的是
   本计划判定为缺陷的行为，列出来停下让 owner 确认后再改，不要静默改测试。
4. Run 入口找不到单一咽喉（多处各自消费 OnlyProject）——报告结构，由 owner 定加固点。

## Out of scope（不要做）

- DeletePreview 返回结构/前端文案改动；"N 对话 + M 空行"双数字方案（owner 可另议）。
- 历史迁移行的**写库式**回填（源文件不在也硬猜项目）；NULL 行自动删除。
- EMPTY_SESSION_FILTER 主体（已实施）；TUI `0×0`、标记行双击（#11/#15，另议）。

## 全量验证闸门

```bash
FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/migrate ./pkg/state ./pkg/tui ./pkg/gateway -count=1
CGO_ENABLED=1 go vet -tags fts5 ./...
gofmt -l cmd pkg third_party
```
