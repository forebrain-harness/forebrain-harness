# Plan 145：项目有三种类型（办公创作 / 创意编程 / 后台跑批），会话出生即冻结类型，项目 runner 按（项目, 类型）构建

> **执行者须知**：按步骤顺序执行，每步跑验证命令并确认预期结果后再进下一步。触发
> "STOP conditions" 时停下报告，不要即兴发挥。完成后把 `plans/README.md` 里本计划的状态行改为 DONE
> （若评审方派发你并说明由其维护索引，则不改）。
> **禁止提交代码**（owner 手动提交）：不 commit、不 push、不开 PR、不建分支、不建 worktree，改动直接留在 dev 工作区。
> 工作区里与本计划无关的未提交改动**不要碰、不要回滚、不要格式化**（只对你改过的文件跑 `gofmt -w`）。
>
> **前置（否则 STOP 1）**：`plans/README.md` 里 `135`–`144` 的状态都不是 `IN PROGRESS`（另一个会话正在改同一批文件时不要开工）。
>
> **Drift check（先跑）**：`git diff --stat d9d0b4d -- pkg/state/schema.sql pkg/state/project_store.go pkg/state/session_store.go pkg/state/schema_migrations.go pkg/process/runner_pool.go pkg/run/runner.go pkg/gateway/api_extra.go`
> 有输出是正常的（当前工作区含有其它任务的未提交改动）。逐段按下文"现状"里的**函数名/SQL 文本**核对；被引用的代码含义变了即 STOP 1。
> 行号是 2026-10-26 工作区的近似值，以函数名为准。
>
> **基线（Step 1 之前跑一次，记下已红的用例；它们不是你要修的）**：
> `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/state ./pkg/process ./pkg/gateway ./pkg/run -count=1`（超时给足 900 s）

## Status

- **Priority**: P1（owner 2026-10-26 新需求：项目类型）
- **Effort**: M
- **Risk**: MED（改状态库结构 + 迁移；改项目 runner 的选取键）
- **Depends on**: none（本批 145–152 的地基，其余七份都依赖它）
- **Category**: direction + migration
- **Planned at**: dev `d9d0b4d` + 2026-10-27 复核工作区（含当前工作区未提交改动）

## owner 裁定（2026-10-26，本计划相关部分）

1. 项目类型三种取值：**办公创作**（`office`）、**创意编程**（`coding`）、**后台跑批**（`batch`）。现在 forebrain 的全部语义都是为创意编程写的，
   创意编程类**保持现状**。
2. 类型**可以改**，但**只影响之后新建的会话**：会话在创建（出生）那一刻冻结所属项目的类型，老会话保持原类型。
3. 已有项目、已有会话迁移后都是 `coding`。**owner 2026-10-27 变更默认规则**：TUI 和 Web 打开一个尚未登记为项目的文件夹时，
   它位于 Git 工作树内（仓库根目录或任意子目录）→ 默认创建 `coding`；不在 Git 工作树内 → 默认创建 `office`。
   `.git` 目录和 linked worktree / submodule 的 `.git` 文件都算 Git 标记；空仓库也算，不执行 `git`、不要求已有 HEAD。
   这条规则只在**新建项目行且请求未显式给类型**时运行：已有项目重开不改类型，已有会话的冻结类型不改，用户显式选择优先，迁移导入的项目仍显式使用 `coding`。
4. 后台跑批类：任何工具都免审批、每次执行在独立的任务工作区里、由**操作系统沙箱强制隔离**（owner 2026-10-26 后续裁定，取代最初的"完全访问"说法，
   细节在 `149`/`150`）；项目级 / 主代理级 / 全局的技能与 MCP 在该项目所有任务间共享。项目级技能、MCP、FOREBRAIN.md 只在**项目受信任**时加载，
   未受信任项目的沙箱也会被收紧（`safety.EffectiveConfig` 按信任推导）。所以本计划规定：**创建为 / 改成 batch 的项目必须同时记录信任**
   （请求带 `trust: true`，或项目已受信任），否则拒绝。
5. 后台跑批类必须禁用记忆功能（owner 追加）。本计划只负责其中"出生"那一半：batch 会话出生时 `memory_mode='disabled'`（不再产生记忆）；
   记忆工具与记忆注入的关闭在 `149`。

## Why this matters

后续七份计划（146–152）的所有差异——系统提示词、计划模式工具、生成物、免审批、只读界面——都要回答"这个会话是什么类型"。
如果类型只放在项目行上，项目一改类型、或者网关重启后老会话重新绑定 runner，老会话就会被悄悄换成新类型的提示词和工具表
（项目 runner 池现在只在内存里记"会话绑定的是哪一代 runner"，重启即丢）。所以类型必须**在会话出生时写进 `fb_sessions`**，
并且 runner 池按"项目 + 会话类型"取 runner，runner 自己知道自己的类型（`run.Deps.ProjectType`），后续计划在 `runner.Load` 里按它分叉。
新默认规则必须在 TUI 的 `AdoptLaunchProject`、Web 的按文件夹建会话和新建项目三条路径上得到同一个答案；
因此由 `ProjectStore` 的创建边界统一判定，而不是在两个前端各自探测 `.git`。

## Current state

### 1. 表结构（`pkg/state/schema.sql`）

`fb_projects`（约 `:8-28`）：

```sql
CREATE TABLE fb_projects (
  id TEXT PRIMARY KEY,
  agent_id TEXT NOT NULL CHECK (TRIM(agent_id) <> ''),
  name TEXT NOT NULL,
  ...
  memory_scope TEXT NOT NULL DEFAULT 'shared' CHECK (memory_scope IN ('shared', 'project_only')),
  resource_access INTEGER NOT NULL DEFAULT 1 CHECK (resource_access IN (0, 1)),
  pinned INTEGER NOT NULL DEFAULT 0 CHECK (pinned IN (0, 1)),
  -- The directory the project's "save to" dialog last uploaded into, ...
  last_attach_dir TEXT NOT NULL DEFAULT '',
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL
) STRICT;
```

`fb_sessions`（约 `:57-80`）里与本计划相关的列：

```sql
  project_id TEXT REFERENCES fb_projects(id) ON DELETE SET NULL,
  origin TEXT NOT NULL DEFAULT 'native' CHECK (origin IN ('native', 'migrated')),
  -- source marks what a session is FOR ...
  source TEXT NOT NULL DEFAULT '',
  cwd TEXT NOT NULL DEFAULT '',
  git_branch TEXT NOT NULL DEFAULT '',
  memory_mode TEXT NOT NULL DEFAULT 'disabled',
```

### 2. 迁移框架（`pkg/state/schema_migrations.go`）

- 当前 `const stateSchemaVersion = 19`（`:21`），迁移表最后一项 `{version: 19, apply: migrateStateV18ToV19}`；v18/v19 已分别被账本与用量耗时占用。
  本计划在**当前源码上**预期使用 v20（`migrateStateV19ToV20`）；执行时若版本又前进，取当时当前值 + 1，并同步迁移函数、测试名和报告。
- "加列"的范例是 `migrateStateV11ToV12`（约 `:564-590`）：先查 `pragma_table_info` 再 `ALTER TABLE ... ADD COLUMN`，
  这样"用当前 schema.sql 建库再把 user_version 调低"的测试夹具不会因重复加列失败：

```go
func migrateStateV11ToV12(ctx context.Context, conn *sql.Conn) error {
	var fileCol int
	if err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_table_info('fb_files') WHERE name='project_path'`).Scan(&fileCol); err != nil {
		return err
	}
	if fileCol == 0 {
		if _, err := conn.ExecContext(ctx, `ALTER TABLE fb_files ADD COLUMN project_path TEXT NOT NULL DEFAULT ''`); err != nil {
			return fmt.Errorf("add fb_files.project_path: %w", err)
		}
	}
	...
}
```

- `TestStateMigrationMatchesFreshSchema`（`schema_migrations_test.go:98`）从 v0 一路迁到当前版本，并与新建库逐个比较 `sqlite_master` 的 SQL 文本——
  v0→v1 是用 schema.sql 重建的，所以只要新迁移"列已存在就跳过"，这条测试天然通过。加列测试的范例是
  `TestStateV11UpgradesToV12AddsProjectFileColumns`（`:646`）：先 `ALTER TABLE ... DROP COLUMN` + `PRAGMA user_version = 11`，再重新 `Open`，断言列存在。
- **注意**：本批 `146` 会紧接着使用下一个版本（预期 v21，`fb_artifacts` 表）；`149`/`151` 再依次取下一版。
  与本批独立的 `183` 也含迁移：若它先落地，本计划顺延，不能覆盖它的版本号或改它的迁移。版本号只在开工时核对一次并记录，不能用旧计划里的数字硬套。

### 3. `pkg/state/project_store.go`

- `Project` 结构体（约 `:32-50`），字段依次为 `ID, AgentID, Name, Icon, Description, Instructions, Root, ProjectKey, MemoryScope, ResourceAccess, Pinned, LastAttachDir, CreatedAt, UpdatedAt`。
- `const projectColumns = \`id, agent_id, name, icon, description, instructions, root, project_key, memory_scope, resource_access, pinned, last_attach_dir, created_at, updated_at\``（约 `:239`），
  `const projectColumnCount = 14`（约 `:241`）。
- `scanProject`（约 `:243-254`）按上面的顺序 `Scan`。
- `CreateProjectInput`（约 `:257-267`）、`Create`（约 `:271-317`，`INSERT INTO fb_projects(`+projectColumns+`) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)`）、
  `FindOrCreateByRoot`（约 `:328-385`，同样 14 个占位符 + `ON CONFLICT(agent_id, root) DO NOTHING`）。
- `UpdateProjectInput` + `Update`（约 `:422-470`），`UPDATE fb_projects SET name=?, icon=?, description=?, instructions=?, memory_scope=?, resource_access=?, updated_at=? WHERE id=? AND agent_id=?`。
- `BindSession`（约 `:707-726`）：

```go
_, err := s.db.ExecContext(ctx, `UPDATE fb_sessions SET project_id=NULLIF(?,'') WHERE id=? AND agent_id=?`, projectID, sessionID, s.agentID)
```

- `ProjectForSession`（约 `:729-749`）读 `fb_sessions.project_id` 再 `Get`。

### 4. 会话出生（`pkg/state/session_store.go` `ensureSession`，约 `:205-270`）

```go
res, err := q.ExecContext(ctx,
	`INSERT INTO fb_sessions(id, title, updated_at, created_at, memory_mode, memory_source, cwd, git_branch, agent_id, source, parent_session_id, project_id)
	VALUES(?, ?, ?, ?, ?, ?,
		COALESCE(NULLIF((SELECT cwd FROM fb_sessions WHERE id=? AND project_id IS NOT NULL), ''), ?),
		?, ?, ?, NULLIF(?, ''),
		COALESCE((SELECT project_id FROM fb_sessions WHERE id=?), NULLIF(?, ''), NULLIF(?, '')))
	ON CONFLICT(id) DO UPDATE SET updated_at=excluded.updated_at
	WHERE fb_sessions.agent_id=excluded.agent_id`,
	id, title, now, now, mode, source,
	cwdParent, cwd,
	branch, s.AgentID(), strings.TrimSpace(birth.Source), parentID,
	parentID, birthProject, defaultProject)
```

出生项目的优先级：父会话的项目 → 调用方给的 `birth.ProjectID` → 进程默认项目（TUI 用 `ConfigureProjectDefault` 设的启动目录项目）。
`ON CONFLICT` 只更新 `updated_at`，所以出生时写下的列以后不会被改——本计划的 `project_type` 正好借这个性质冻结。
网关建会话走 `createSessionWithBirth`（`pkg/gateway/api_extra.go` 约 `:1321`），`birth.ProjectID` 已是项目 id，之后再调 `bindSessionToProject` → `ProjectStore.BindSession`。

### 5. 项目 runner 池（`pkg/process/runner_pool.go`）

- 文件头注释（`:1-34`）写明冻结规则："A project edit afterwards creates a new entry for new sessions; the bound session keeps the old one"。
  但"绑定哪一代"只记在内存 `p.sessions` 里，重启即丢——所以老会话重启后会拿到按**当前**项目行建的 runner。类型不能依赖这个。
- `entryForSession(ctx, sessionID, project)`（约 `:784-821`）：先看内存绑定，否则 `key := poolKey(p.env.Deps.AgentName, project)`，没有就 `buildEntryLocked`。
  `RunnerForSession`、`RunnerForTurn`、`BindSession`（池的，约 `:1233-1253`）都经它。
- `BoundRunnerForSession`（约 `:625-656`）不建 entry，自己算 `poolKey` 查表。
- `poolKey`（约 `:823-836`）：

```go
body := strings.Join([]string{
	strings.TrimSpace(project.ID),
	strings.TrimSpace(project.Name),
	strings.TrimSpace(project.Root),
	strings.TrimSpace(project.ProjectKey),
	strings.TrimSpace(project.Instructions),
	state.NormalizeProjectMemoryScope(project.MemoryScope),
	fmt.Sprintf("%t", project.ResourceAccess),
	fmt.Sprintf("%d", project.UpdatedAt),
}, "\x1f")
```

- `buildEntryLocked`（约 `:942-1076`）构造 `run.Deps{...}`，随后 `deps.ProjectMemoryOnly = ...`、`deps.ProjectInstructionsFor = ...`。工坊 entry 调用时 `project` 只有 `Root`。

### 6. `pkg/run/runner.go`

- `type Deps struct`（约 `:47`）有 `ProjectKey`、`ProjectRoot`、`ProjectMemoryOnly`、`LaunchProject` 等字段。
- 访问器范例 `func (r *Runner) ProjectMemoryOnly() bool`（约 `:3559-3564`）：

```go
func (r *Runner) ProjectMemoryOnly() bool {
	if r == nil || r.Deps == nil {
		return false
	}
	return r.Deps.ProjectMemoryOnly
}
```

### 7. 网关（`pkg/gateway/api_extra.go`）

- `type projectPayload struct`（约 `:6630-6640`）：`Name, Icon, Description, Instructions, Root, Trust, MemoryScope, ResourceAccess *bool, Pinned *bool`。
- `projectRow`（约 `:6658-6679`）返回 map，含 `trust_level`、`missing`。
- `handleProjectsCreate`（约 `:6770-6834`）：校验 root、name → `store.Create` → `body.Trust` 时 `safety.MarkTrusted(s.Home, safety.Project{Root: root})`，
  返回 `row["trust_recorded"]`。
- `handleProjectOne` 的 PATCH（约 `:6856-6883`）：组 `state.UpdateProjectInput` → `store.Update` → `body.Trust` 时 `MarkTrusted`。
- 信任判断函数 `projectIsTrusted(home, root string) bool`（约 `:6644-6656`）。
- 会话详情 `handleChatSession`（约 `:1683-1698`）：

```go
project = map[string]any{"id": p.ID, "name": p.Name, "root": p.Root, "missing": !hostDirExists(p.Root), "last_attach_dir": p.LastAttachDir}
...
writeJSON(w, map[string]any{"id": sid, "title": title, "project": project})
```

- 项目会话列表 `handleProjectSessionsList` 分页分支（约 `:7071-7086`）每行是 `map[string]any{"id", "title", "created_at", "updated_at", "model", "message_count", ...}`。

### 8. 约束（架构测试，`pkg/architecture/cache_test.go`）

- `TestPackagesStayUnderThirtyProductionFiles`：每包生产文件 ≤ 31。现状 `pkg/gateway` **已是 31**、`pkg/state` 24、`pkg/process` 20、`pkg/run` 25。
  **本计划不新建任何生产文件**，全部加在现有文件里。
- `TestTestFilesCorrespondToProductionFiles`：`X_test.go` 必须有同名 `X.go`。测试加进现有的 `*_test.go`。
- `fanOutBudgets`：各包的包内导入数不许增长。本计划不新增任何跨包 import。

## Commands you will need

| 目的 | 命令 | 成功时 |
|------|------|--------|
| 编译 | `go build ./...` | exit 0 |
| vet | `go vet ./pkg/state ./pkg/process ./pkg/run ./pkg/gateway` | exit 0 |
| 状态库测试 | `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/state -count=1` | ok |
| 池测试 | `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/process -run 'TestRunnerPool\|TestBoundRunner\|TestEnvironmentBind' -count=1` | ok |
| 网关测试 | `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/gateway -run 'TestProject\|TestChatSession' -count=1` | ok |
| 架构 | `CGO_ENABLED=1 go test -tags fts5 ./pkg/architecture -count=1` | ok |
| 格式 | `gofmt -l pkg/state pkg/process pkg/run pkg/gateway` | 只列出你**没改过**的文件或无输出 |

## Scope

**In scope**：
- `pkg/state/schema.sql`、`pkg/state/schema_migrations.go`、`pkg/state/schema_migrations_test.go`
- `pkg/state/project_store.go`、`pkg/state/project_store_test.go`
- `pkg/state/session_store.go`（只改 `ensureSession` 的 INSERT 与 `ProjectSessionRow` / `projectSessionsPageSQL` / `ListSessionsForProjectPage`）、`pkg/state/session_store_test.go`
- `pkg/process/runner_pool.go`、`pkg/process/runner_pool_test.go`
- `pkg/run/runner.go`（只加 `Deps.ProjectType` 字段与访问器）
- `pkg/gateway/api_extra.go`、`pkg/gateway/api_extra_test.go`
- `pkg/migrate/sessions.go`（两处 `FindOrCreateByRoot` 的 defaults 显式补 `ProjectType: state.ProjectTypeCoding`，保证迁移导入的旧项目不受新自动默认影响）
- `pkg/tui/chat_session_test.go`（只为 TUI 真实 `AdoptLaunchProject` 默认类型补测试；生产 TUI 无需改，复用共享 store 判定）

**Out of scope**（不要碰）：
- 任何按类型改行为的逻辑（提示词、工具表、审批、只读、生成物）——那是 146–153 的事；本计划只让"类型"可读、可判定、可冻结、可传到 runner。
- 前端（`frontend/`）——web 的类型选择与默认类型展示在 `153`，且要先过原型。
- TUI 跑批查看器——在 `152`。
- `pkg/safety` 的信任语义本身。

## Steps

### Step 1：类型常量与规范化（`pkg/state/project_store.go`）

在 `ErrProjectBusy` 声明附近加：

```go
// The three kinds of project. A project's type decides how its conversations
// run: office and batch get their own system prompts and deliverables, batch
// runs unattended behind its OS task sandbox, and coding is everything forebrain did
// before project types existed. A conversation freezes its project's type when
// it is born (fb_sessions.project_type), so changing a project's type only
// reaches the conversations started after the change.
const (
	ProjectTypeOffice = "office"
	ProjectTypeCoding = "coding"
	ProjectTypeBatch  = "batch"
)

// ErrProjectTypeInvalid reports a project type outside the three kinds.
var ErrProjectTypeInvalid = errors.New("project type must be office, coding or batch")

// NormalizeProjectType maps a stored or empty value onto one of the three
// kinds; anything unknown reads as coding, the behaviour every conversation had
// before types existed.
func NormalizeProjectType(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case ProjectTypeOffice:
		return ProjectTypeOffice
	case ProjectTypeBatch:
		return ProjectTypeBatch
	default:
		return ProjectTypeCoding
	}
}

// ParseProjectType is NormalizeProjectType for input a caller supplied: empty
// is coding, an unknown value is refused instead of silently becoming coding.
func ParseProjectType(raw string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "":
		return ProjectTypeCoding, nil
	case ProjectTypeOffice, ProjectTypeCoding, ProjectTypeBatch:
		return strings.ToLower(strings.TrimSpace(raw)), nil
	default:
		return "", ErrProjectTypeInvalid
	}
}
```

再在同一个文件加入创建期默认判定。**不要**改 `ParseProjectType("") == coding` 的存储/旧会话兼容语义；
只有“新建项目行”调用下面的 root-aware 解析：

```go
// DefaultProjectTypeForRoot applies the launch default for a folder that has
// no project yet: coding anywhere in a Git worktree, office elsewhere. The
// marker rules mirror memory/assembly (.git directory or worktree file), so
// subdirectories, empty repositories and linked worktrees need no git binary.
func DefaultProjectTypeForRoot(root string) (string, error)

// ResolveProjectTypeForRoot honors an explicit project type first. Only an
// omitted type uses DefaultProjectTypeForRoot.
func ResolveProjectTypeForRoot(raw, root string) (string, error)
```

实现约束：
- `DefaultProjectTypeForRoot` 先 `canonicalProjectRoot(root)`；从该目录向父目录逐级检查 `<dir>/.git`。
  `os.Stat` 成功且 `IsDir() || Mode().IsRegular()` 即返回 `coding`；到文件系统根仍无标记返回 `office`。
  这与 `pkg/memory/scope.go:342-348` / `pkg/assembly/rules.go:53-59` 已核实的 marker 语义一致，重复这几行以避免 `state → memory` 的 import cycle。
  不执行 `git rev-parse`，不读取 `.git` 文件内容，不要求 HEAD；不得以 `memory.GitBranch` 判定（空仓库会误判为 office）。
- `ResolveProjectTypeForRoot`：`strings.TrimSpace(raw) != ""` 时只调用 `ParseProjectType(raw)`；为空时调用默认判定。
  因而显式 `coding`/`office`/`batch` 永远覆盖自动判定。
- `Create` 和 `FindOrCreateByRoot` 只在**实际准备插入新行**时调用它；`FindOrCreateByRoot` 先查已有行并原样返回，已有项目绝不重判。
  `Create` 遇到 `ErrProjectRootTaken` 也返回既有行，不用新默认覆盖。
- `pkg/migrate/sessions.go` 两处 defaults 显式 `ProjectType: state.ProjectTypeCoding`，因为导入的是旧编程会话，不是用户刚打开的新文件夹。

**Verify**：`go build ./pkg/state ./pkg/migrate` → exit 0；`FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/state -run 'ProjectType.*Root|DefaultProjectType' -count=1` → ok。

### Step 2：表结构与迁移（预期 v20，执行时取当前 + 1）

1. `pkg/state/schema.sql`：
   - `fb_projects` 在 `last_attach_dir ...` 之后、`created_at` 之前加一列（带注释）：
     ```sql
       -- What kind of project this is (office | coding | batch). A conversation
       -- copies it into fb_sessions.project_type when it is born, so changing it
       -- only reaches the conversations started afterwards.
       project_type TEXT NOT NULL DEFAULT 'coding' CHECK (project_type IN ('office', 'coding', 'batch')),
     ```
   - `fb_sessions` 在 `source TEXT NOT NULL DEFAULT '',` 之后加：
     ```sql
       -- The project type this conversation was born with (office | coding |
       -- batch), frozen at birth like source and agent_id.
       project_type TEXT NOT NULL DEFAULT 'coding' CHECK (project_type IN ('office', 'coding', 'batch')),
     ```
2. `pkg/state/schema_migrations.go`：
   - 当前基线预期：`stateSchemaVersion` 改为 `20`；迁移表末尾加 `{version: 20, apply: migrateStateV19ToV20},`，在 `migrateStateV18ToV19` 后新增函数。
     若开工时版本已前进，则改为“当时当前值 + 1”，不得与 `183` 或其它已落地迁移撞号。
   - 新迁移照 `migrateStateV11ToV12` 的写法对两张表各做一次"查列 → 不存在才 ADD COLUMN"，
     ADD COLUMN 的列定义与 schema.sql 里**逐字相同**（`project_type TEXT NOT NULL DEFAULT 'coding' CHECK (project_type IN ('office', 'coding', 'batch'))`）。
     注释写明：纯加列，所有既有项目与会话读作 `coding`（owner 2026-10-26：已有项目迁移后都是创意编程）。

**Verify**：`FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/state -run 'TestStateMigrationMatchesFreshSchema|TestStateOpenIsIdempotentAtCurrentVersion|TestFreshStateSchema' -count=1` → ok。

### Step 3：`Project` 读写带上类型

`pkg/state/project_store.go`：
1. `Project` 结构体在 `LastAttachDir` 之后加 `ProjectType string`（注释：项目当前类型；会话的类型以 `fb_sessions.project_type` 为准）。
2. `projectColumns` 在 `last_attach_dir` 之后加 `project_type`，`projectColumnCount` 改 15；`scanProject` 相应多扫一个 `&p.ProjectType`，扫完 `p.ProjectType = NormalizeProjectType(p.ProjectType)`。
3. `CreateProjectInput` 加 `ProjectType string`。`Create`：调用 `ResolveProjectTypeForRoot(in.ProjectType, root)`，错误直接返回；写进 `p.ProjectType`；INSERT 占位符改 15 个、参数按列顺序补上。
4. `FindOrCreateByRoot`：查到已有项目先原样返回；只有需要插入时调用 `ResolveProjectTypeForRoot(defaults.ProjectType, canonical)`，INSERT 补列。
   TUI `AdoptLaunchProject` 与 Web 的 `handleChatSessionCreate(folder=...)` 已共用此入口，不传类型即可获得同一新默认，**不要**在两处复制 `.git` 探测。
5. `UpdateProjectInput` 加 `ProjectType *string`。`Update`：非 nil 时 `ParseProjectType`，错误返回；UPDATE 语句加 `project_type=?`。
   项目 PATCH 不调用自动默认：用户显式修改类型才走这里。
6. `BindSession` **保持现有 SQL，不重写 `project_type`**。会话在 `ensureSession` 已按出生项目冻结类型；
   后续再绑定（包括重复绑定、恢复、其它入口补绑定）不能借“换项目”改变老会话类型，否则违背 owner“只影响新会话”的裁定。
7. 新增：
   ```go
   // SessionProjectType returns the project type a conversation was born with.
   // A session this agent does not own, or one that does not exist, reads as
   // coding — the behaviour of every conversation before types existed.
   func (s *ProjectStore) SessionProjectType(ctx context.Context, sessionID string) (string, error)
   ```
   查询 `SELECT project_type FROM fb_sessions WHERE id=? AND agent_id=?`，`sql.ErrNoRows` → `(ProjectTypeCoding, nil)`，结果过 `NormalizeProjectType`。

**Verify**：`go build ./...` → exit 0；`FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/state -count=1` → ok（既有用例不应变红）。

### Step 4：会话出生冻结类型；batch 会话出生即不产生记忆

`pkg/state/session_store.go` `ensureSession` 的 INSERT：列清单末尾加 `project_type`，VALUES 末尾加一个表达式，参数末尾补 3 个（`parentID, birthProject, defaultProject`）：

```sql
COALESCE(
  (SELECT project_type FROM fb_sessions WHERE id=? AND project_id IS NOT NULL),
  (SELECT project_type FROM fb_projects WHERE id = COALESCE(NULLIF(?, ''), NULLIF(?, ''))),
  'coding')
```

同时 `memory_mode` 那个占位符改成"batch 出生就是 disabled"：把 VALUES 里第 5 个 `?`（`mode`）换成

```sql
CASE WHEN COALESCE(
  (SELECT project_type FROM fb_sessions WHERE id=? AND project_id IS NOT NULL),
  (SELECT project_type FROM fb_projects WHERE id = COALESCE(NULLIF(?, ''), NULLIF(?, ''))),
  'coding') = 'batch' THEN 'disabled' ELSE ? END
```

（参数在 `mode` 的位置前插入 `parentID, birthProject, defaultProject`，`mode` 保持在 `ELSE ?`。）
父会话子查询里的 `AND project_id IS NOT NULL` 必须保留：只有父会话真的属于一个项目时才继承它的冻结类型；
无项目的父会话不能让默认 `coding` 覆盖本次显式的出生项目。
**不要**动 `ON CONFLICT ... DO UPDATE SET updated_at=excluded.updated_at` 那行——出生后不改类型、也不改 memory_mode，正是靠它。
在 INSERT 上方的注释段补两句：类型与父会话 / 出生项目同源；batch 会话不生成记忆（owner 2026-10-26）。

**Verify**：`go build ./pkg/state` → exit 0。

### Step 5：runner 知道自己的类型

`pkg/run/runner.go`：
1. `type Deps struct` 在 `ProjectMemoryOnly bool` 之后加：
   ```go
   // ProjectType is the type of the conversations this runtime serves
   // (state.ProjectTypeOffice/Coding/Batch): the type each one froze at birth,
   // never the project's current type. The runner pool keys its entries by it,
   // so one runner never serves two types. Empty reads as coding.
   ProjectType string
   ```
2. 仿照 `ProjectMemoryOnly()` 加访问器：
   ```go
   // ProjectType is this runtime's conversation type, coding when unset.
   func (r *Runner) ProjectType() string {
   	if r == nil || r.Deps == nil {
   		return state.ProjectTypeCoding
   	}
   	return state.NormalizeProjectType(r.Deps.ProjectType)
   }
   ```
   （`pkg/run` 已 import `pkg/state`，不新增 import。）

**Verify**：`go build ./...` → exit 0。

### Step 6：池按"项目 + 会话类型"取 runner

`pkg/process/runner_pool.go`：
1. `poolKey` 的 `body` 切片末尾加 `state.NormalizeProjectType(project.ProjectType),`。注释补一句：类型是会话出生时冻结的那个（见下）。
2. 新增私有方法：
   ```go
   // withSessionType returns project with ProjectType replaced by the type the
   // session was born with. A conversation keeps its birth type across project
   // edits and process restarts, so the entry it resolves to is chosen by that
   // type, never by the project's current one.
   func (p *RunnerPool) withSessionType(ctx context.Context, sessionID string, project state.Project) state.Project
   ```
   实现：`p.projects == nil` 或 `sessionID == ""` 时原样返回；否则 `t, err := p.projects.SessionProjectType(ctx, sessionID)`，err 时原样返回，否则 `project.ProjectType = t`。
3. `entryForSession` 第一行（**在取 `p.mu` 之前**）加 `project = p.withSessionType(ctx, sessionID, project)`。
4. `BoundRunnerForSession` 里 `key := poolKey(p.env.Deps.AgentName, project)` 之前加同一行。
5. `buildEntryLocked` 构造 `deps` 之后（`deps.ProjectMemoryOnly = ...` 那附近）加 `deps.ProjectType = state.NormalizeProjectType(project.ProjectType)`。
   工坊 entry 传入的 `state.Project{Root: skillsDir}` 没有类型 → `coding`，不用特判。
6. 文件头"Freezing rules"注释补一条：会话出生时冻结项目类型（`fb_sessions.project_type`），entry 按它分代，所以改项目类型只影响新会话、重启后也不漂移。

**Verify**：`go build ./...` → exit 0；`FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/process -run 'TestRunnerPool|TestBoundRunner|TestEnvironmentBind' -count=1` → ok。

### Step 7：网关读写项目类型

`pkg/gateway/api_extra.go`：
1. `projectPayload` 加 `ProjectType string \`json:"project_type"\``。
2. `projectRow` 加 `"project_type": state.NormalizeProjectType(p.ProjectType),`。
3. `handleProjectsCreate`：在 `store.Create` 之前
   ```go
   projectType, err := state.ResolveProjectTypeForRoot(body.ProjectType, root)
   if err != nil {
   	writeJSONStatus(w, http.StatusBadRequest, map[string]any{"error": "project_type_invalid"})
   	return
   }
   if projectType == state.ProjectTypeBatch && !body.Trust {
   	// A batch project's tasks run unattended on the project's own skills
   	// and MCP servers, which load only in a trusted project, and its
   	// sandbox is derived from that trust. Creating one therefore records
   	// the trust decision with it.
   	writeJSONStatus(w, http.StatusConflict, map[string]any{"error": "batch_requires_trust"})
   	return
   }
   ```
   并把 `ProjectType: projectType` 传进 `CreateProjectInput`。**网关没有 `writeJSONStatus`**（只有不带状态码的 `writeJSON`，`api_extra.go` 约 `:7784`）：
   上面伪代码里的 `writeJSONStatus(w, code, body)` 一律展开成该文件已有的写法
   `w.Header().Set("Content-Type", "application/json"); w.WriteHeader(code); _ = json.NewEncoder(w).Encode(body)`，不要新建帮助函数。
   **注意**：batch 时 `body.Trust` 为 true，现有代码随后会 `MarkTrusted`；如果 `MarkTrusted` 失败（`trust_recorded=false`），对 batch 项目返回 500 `{"error":"trust_not_recorded"}` 并**删除刚建的项目**（`store.Delete`），避免留下一个无法按约定受信任并强制沙箱隔离的 batch 项目。
4. PATCH：`body.ProjectType != ""` 时 `ParseProjectType`（错误 → 400 `project_type_invalid`）；目标是 batch 且 `!body.Trust && !projectIsTrusted(s.Home, p.Root)` → 409 `batch_requires_trust`；
   否则 `in.ProjectType = &projectType`。保留现有 `body.Trust` 时 `MarkTrusted` 的逻辑。
5. 会话详情 `handleChatSession`：返回体加顶层 `"project_type"`，值取 `store.SessionProjectType(r.Context(), sid)`（出错按 500 处理，与该函数现有的错误处理一致）；
   `project` map 里加 `"project_type": state.NormalizeProjectType(p.ProjectType)`（项目**当前**类型，界面可用来提示"项目已改类型，本会话仍是旧类型"）。
6. 项目会话列表分页分支的每行加 `"project_type"`。数据来自 `pkg/state/session_store.go`：
   - `type ProjectSessionRow struct`（约 `:975-987`）在 `MessageCount int` 之后加 `ProjectType string`；
   - `projectSessionsPageSQL`（约 `:1035-1046`）的 SELECT 列表在 `projectSessionMessageCount` 之后加 `, fb_sessions.project_type`；
   - `ListSessionsForProjectPage`（约 `:1053-1086`）的 `rows.Scan(...)` 末尾加 `&item.ProjectType`，扫完 `item.ProjectType = NormalizeProjectType(item.ProjectType)`；
   - 网关分页分支的行 map 加 `"project_type": item.ProjectType`。
7. **默认类型只读接口（供 Web 新建项目弹窗预选，153 消费）**：在 `projects` 路由组把静态路由放在 `/:id` 前：
   ```go
   projects.Get("/default-type", s.handleProjectDefaultType)
   ```
   `GET /api/v1/projects/default-type?root=<绝对目录>`：`state.ValidateProjectRoot(s.Home, root)` 后调用 `DefaultProjectTypeForRoot`，返回
   `{"project_type":"office|coding","source":"git_worktree|non_git"}`；无 root / 目录校验失败 → 400，探测本身的其它错误 → 500。
   它只读文件系统 marker，不创建项目、不写信任记录；`source` 仅用于界面解释“Git 工作树默认创意编程 / 非 Git 默认办公创作”。
   `handleChatSessionCreate` 按 folder 自动创建时**不调用该 HTTP 接口**，直接共享 `FindOrCreateByRoot` 的判定。

**Verify**：`go build ./...` → exit 0；`FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/gateway -run 'TestProject|TestChatSession' -count=1` → ok。

### Step 8：测试

见下方 Test plan，全部写完后跑 Done criteria 的整组命令。

## Test plan

1. `pkg/state/schema_migrations_test.go` 新增 `TestStateV19UpgradesToV20AddsProjectTypeColumns`（若版本顺延则同步改名），照 `TestStateV11UpgradesToV12AddsProjectFileColumns` 写：
   建库 → 插一个项目和一个会话（用 `NewProjectStore(...).Create` 与 `NewSessionStore(...).EnsureAt`）→ `ALTER TABLE fb_sessions DROP COLUMN project_type`、
   `ALTER TABLE fb_projects DROP COLUMN project_type`、`PRAGMA user_version = 19`（或前一版）→ 关库重开 → 断言版本 = `stateSchemaVersion`、两列存在、旧行都是 `'coding'`。
2. `pkg/state/project_store_test.go`：
   - `TestDefaultProjectTypeForRoot`：非 Git 目录 → office；新建 `.git` 目录的仓库根及其深层子目录 → coding；只有 `.git` 文件的 linked-worktree 形式 → coding；空仓库（无 HEAD）→ coding。
   - `TestProjectCreateDefaultsFromRootAndHonorsExplicitType`：非 Git 不传类型 → office；Git 子目录不传类型 → coding；非 Git 显式 coding → coding；Git 显式 office → office；传 `"BATCH"` 规范化；传 `"x"` → `ErrProjectTypeInvalid`。
   - `TestFindOrCreateByRootDoesNotReclassifyExistingProject`：首次非 Git 默认 office；在目录里随后加入 `.git` 或在调用 defaults 里显式传 coding，再次 `FindOrCreateByRoot` 都返回既有 office，`created=false`。
   - `TestProjectUpdateChangesType`：`Update` 把 coding 改 office 后 `Get` 读到 office；非法值报错且行不变。
   - `TestSessionFreezesProjectTypeAtBirth`：office 项目 → `EnsureAt(..., SessionBirth{ProjectID: p.ID})` 的会话读 `office`；把项目改 coding；老会话仍 `office`，新会话 `coding`；
     再对老会话调一次 `Ensure`（模拟后续写入）仍是 `office`。
   - `TestChildSessionInheritsParentProjectType`：父会话 office，子会话（`ParentSessionID`）即使 `birth.ProjectID` 指向一个 coding 项目也读 `office`。
   - `TestBatchSessionIsBornWithMemoryDisabled`（放 `session_store_test.go`）：`sessions.ConfigureMemoryDefaults("enabled", "", "", "")`（签名 `ConfigureMemoryDefaults(memoryMode, memorySource, cwd, gitBranch string)`，`session_store.go:110`），
     batch 项目的新会话 `SELECT memory_mode` 仍是 `disabled`，coding 项目的新会话是 `enabled`。
   - `TestBindSessionNeverChangesFrozenProjectType`：office 出生的会话，项目改 coding 后重复 `BindSession` 仍 office；即便之后绑到另一个 batch 项目，`fb_sessions.project_type` 仍 office（项目归属可变，出生类型不可变）。
3. `pkg/process/runner_pool_test.go` 新增 `TestRunnerPoolKeysEntriesBySessionType`，照 `TestRunnerPoolProjectEditFreezesExistingSessions` 写：
   office 项目、会话 A 绑定 → `RunnerForSession(A).ProjectType()=="office"`；项目改 coding、会话 B 绑定 → B 的 runner 不同且 `ProjectType()=="coding"`；
   **新建一个池**（`pool.Close()` 后 `NewRunnerPool(env, 4)`，模拟重启）→ A 仍拿到 `ProjectType()=="office"` 的 runner。
4. `pkg/tui/chat_session_test.go` 照 `TestAdoptLaunchProjectRegistersTheLaunchDirectory` 增加 `TestAdoptLaunchProjectUsesGitAwareDefaultType`：
   非 Git 启动目录经真实 `AdoptLaunchProject` → 新行 office；Git 子目录 → coding；再次 Adopt 不改已有类型。
5. `pkg/gateway/api_extra_test.go`（照 `TestProjectsCreateTrustIsExplicitOperatorChoice` 的 `newProjectsTestServer` + `doJSON` 写）：
   - `TestProjectsCreateCarriesProjectType`：显式建 office → 201 且 `row["project_type"]=="office"`；GET 详情一致。
   - `TestProjectDefaultTypeEndpoint`：非 Git root 返回 office/non_git，Git 子目录返回 coding/git_worktree；请求不产生项目行。
   - `TestProjectsCreateAndFolderSessionUseGitAwareDefaults`：`POST /api/v1/projects` 不传类型，以及 `POST /api/chat/sessions` 传新 folder，两类入口都遵循 Git→coding、非 Git→office；已有项目重开不变。
   - `TestProjectsCreateRejectsUnknownProjectType`：`"project_type":"x"` → 400，body 含 `project_type_invalid`。
   - `TestProjectsBatchRequiresTrust`：batch 不带 trust → 409 `batch_requires_trust` 且项目**未**创建（列表为空）；带 `trust:true` → 201、`safety.IsTrusted` 为 true。
   - `TestProjectPatchTypeReachesOnlyNewSessions`：office 项目建会话 S1（`POST /api/v1/projects/:id/sessions`）→ PATCH 成 coding → 建 S2 →
     `GET /api/chat/sessions/S1` 的 `project_type=="office"`、S2 为 `coding`，S1 的 `project.project_type=="coding"`。
   - `TestProjectPatchToBatchRequiresTrust`：未信任的 coding 项目 PATCH 成 batch 不带 trust → 409；带 `trust:true` → 200。

## Done criteria

- [ ] `go build ./...`、`go vet ./pkg/state ./pkg/process ./pkg/run ./pkg/gateway ./pkg/migrate ./pkg/tui` exit 0
- [ ] `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/state ./pkg/process ./pkg/run ./pkg/gateway ./pkg/migrate ./pkg/tui ./pkg/architecture -count=1` 无**新增**失败（与基线比）；Test plan 列出的新测试都存在且通过
- [ ] `grep -n "stateSchemaVersion = 20" pkg/state/schema_migrations.go` 有 1 条（或按 Current state §2 的说明为"执行时当前值+1"）
- [ ] `grep -c "project_type TEXT NOT NULL DEFAULT 'coding'" pkg/state/schema.sql` = 2
- [ ] `grep -n "NormalizeProjectType(project.ProjectType)" pkg/process/runner_pool.go` ≥ 2 条（poolKey 与 buildEntryLocked）
- [ ] `ls pkg/gateway/*.go | grep -v _test.go | wc -l` 仍是 31；`ls pkg/state/*.go | grep -v _test.go | wc -l` 仍是 25
- [ ] `gofmt -l` 对你改过的文件无输出
- [ ] `plans/README.md` 本计划状态行已更新

## STOP conditions

1. 前置不满足，或"现状"里的函数 / SQL 文本对不上。
2. `ensureSession` 的 INSERT 参数顺序与上文不同（例如列清单变了）——不要猜参数位置，停下报告。
3. 加完列后 `TestStateMigrationMatchesFreshSchema` 红：说明迁移路径里有一步不是"由 schema.sql 重建"，停下报告失败输出，不要改那条测试。
4. 改 `poolKey` 后有既有池测试红，且原因不是"类型不同所以 key 不同"——停下报告。
5. 发现某个建会话的路径既不给 `birth.ProjectID`、也不走 `BindSession`、也不是子会话（类型会落成 coding 但它其实属于 office/batch 项目）——列出该路径并停下，不要自行补。
6. 任何一步需要新建生产文件或新增跨包 import（会撞架构测试）。

## Maintenance notes

- 会话类型是**出生冻结**的：以后任何"按类型分叉"的代码都必须读会话类型（`ProjectStore.SessionProjectType` / `Runner.ProjectType()`），**不要读项目当前类型**。
  唯一例外是"新建会话 / 新建任务"这类出生动作本身。
- **默认类型只在项目创建时求值一次**：Git 工作树（含子目录与 `.git` 文件）→ coding，非 Git → office；显式选择优先。
  后续增删 `.git` 不会改已有项目，更不会改已有会话。`ParseProjectType("") == coding` 仅保留给旧存储/无项目会话兼容，不得拿它替代创建期的 root-aware 判定。
- runner 池现在按 `(项目, 会话类型, 冻结字段指纹)` 分代。改项目类型 = 生成新一代 entry；老会话仍落在旧类型的 entry 上（重启后照样按出生类型重建）。
- batch 必须受信任：它的任务依赖项目级技能 / MCP（只在受信任项目加载），沙箱也按信任推导。跑批的免审批 + 沙箱隔离在 `149`/`150`。
- 本计划只让 batch 会话"出生不产生记忆"；记忆工具不暴露、记忆不注入在 `149`。
