# Plan 025：状态库——每个会话归入项目、项目根唯一、去掉归档、删除项目连同对话、项目键按项目根本身

> **执行者须知**：按步骤顺序执行，每步跑验证命令并确认预期结果后再进下一步。触发
> "STOP conditions" 时停下报告，不要即兴发挥。**不要更新 `plans/README.md`**（评审方维护索引）。
> **禁止提交代码**（owner 手动提交）：不建分支、不 commit、不 push、不 stash；改动留在工作区。
> **不要碰 owner 的真实状态库** `~/.forebrain/state/forebrain.state.sqlite`：测试一律用临时目录；需要本机数据时只能 `sqlite3 -readonly`。
>
> **Drift check（先跑）**：
> - `grep -n 'const stateSchemaVersion' pkg/state/schema_migrations.go` → 记下当前版本 N（写计划时为 8；015、019 也加迁移，谁先执行谁取下一个号，本计划取 **N+1**）。
> - `grep -n 'archived_at' pkg/state/schema.sql` 有 1 行；`grep -n 'func (s \*ProjectStore) SetArchived' pkg/state/project_store.go` 有 1 行。
> - `grep -n 'CREATE INDEX idx_fb_projects_agent_root' pkg/state/schema.sql` 有 1 行（非唯一索引）。
> 任一不符 → STOP。

## Status

- **Priority**: P1
- **Effort**: M
- **Risk**: HIGH（迁移删除用户数据、重建项目归属）
- **Depends on**: 无代码依赖；与 015/019 的迁移版本号顺延
- **Category**: migration / data model
- **Planned at**: commit `005c1a8` + 主树未提交改动，2026-10-11

## Why this matters

owner 2026-10-10～11 定的项目模型（设计 `docs/plan/SESSION_BIRTH_DIR_CONTINUATION_DESIGN.md` 第 0 节 R1、R9、R11 与 2.7b、2.8）：
- **每个会话都属于一个项目**，出生目录 = 项目根 = 工作目录。唯一例外是主代理级技能工坊会话（`source='workshop'`，工作目录是主代理技能目录）。
- 老库里没有项目的会话，按 `cwd` 原样建项目并归入，**目录不存在也照样建**；只删 `cwd` 为空的会话。
- **去掉项目归档**；**删除项目 = 连同它的对话、定时任务一起删**，目录里的文件不动；项目里有对话在运行时拒绝删除。
- `(agent_id, root)` 唯一：TUI 与 web 可能同时登记同一个目录。
- **项目键按项目根本身生成**（owner 2026-10-11）：不再沿目录向上找 git 根。今天的 `memory.ProjectKey` 会找（`pkg/memory/scope.go:229-253`），仓库根和它的子目录项目因此共用一个键，项目记忆会串。
- 迁移结束照框架现有行为，有空页就 VACUUM（owner 2026-10-11 撤回了"不自动回收"）。

## Current state（全部已核实）

### schema 与迁移框架

- `fb_projects`（`pkg/state/schema.sql:7-24`）有 `archived_at INTEGER` 列，索引 `idx_fb_projects_agent_root ON fb_projects(agent_id, root)` 不是唯一索引。
- `fb_sessions.project_id ... ON DELETE SET NULL`（`:36`），`parent_session_id ... ON DELETE SET NULL`（`:35`），`source TEXT`（`:41`，`'workshop'` 为工坊、`'subagent'` 为 subagent）。
- 引用 `fb_sessions` 的表：
  - 用 `session_id` 引用的：`fb_messages`（`:63`）、`fb_session_ui_state`（`:91`）、`fb_session_model_state`（`:103`）、`fb_session_prompt_state`（`:120`）、`fb_actions`（`:132`）、`fb_runs`（`:146`）、`fb_session_events`（`:195`）、`fb_files`（`:232`）、`fb_heartbeats`（`:351`）；
  - `fb_memory_stage1_outputs` 用 `(thread_id, agent_id)` 引用（`:270`）；
  - `fb_run_waits` 引用 `fb_runs` 与 `fb_actions`（`:211-219` 一带）；
  - `fb_cron_runs.session_id` 没有外键（`:333` 一带）；
  - `fb_memory_jobs` 用 `kind='memory_stage1' AND job_key=<会话 id>` 关联，没有外键。
- `fb_cron_jobs.project_id ... ON DELETE SET NULL`（`:303`）。
- **迁移在外键关闭的状态下运行**：`migrateStateSchema`（`pkg/state/schema_migrations.go:306` 起）先 `PRAGMA foreign_keys = OFF`，再 `BEGIN IMMEDIATE`。所以迁移里 `DELETE FROM fb_sessions` **不会级联**，子表必须逐个显式删除。迁移末尾 `requireNoForeignKeyViolations` 会对任何遗留的孤儿行报错。
- 迁移末尾：`freelist_count > 0` 时 VACUUM（`:400-410` 一带）。**保留不动**。
- **旧迁移会执行当前的 `schemaSQL`**：`migrateStateV0ToV1`（`:506` 起）把旧表改名后执行 `schemaSQL`，再逐表复制；复制项目的 `migrateV1Projects`（`:789-826`）往新表写 `archived_at`（`:796`、`:802`）。schema.sql 去掉这一列后，这段复制必须同步改，否则 v0 库升级会失败。
- 增量迁移的写法：先用 `pragma_table_info` 判断对象是否已存在，存在就跳过。原因是测试会用当前 `schema.sql` 建库、再把 `user_version` 调低来模拟旧库。样例见 `migrateStateV3ToV4`（`:55-69`）。
- 版本表 `stateSchemaMigrations`（`:38-47`），常量 `stateSchemaVersion`（`:18`）。

### 项目存储

`pkg/state/project_store.go`：
- `Project.ArchivedAt`（`:39-40`）；`projectColumns` 含 `archived_at`（`:77`）；
- `Create`（`:110-148`）写 `ArchivedAt: nil`；
- `GetByRoot`（`:173-187`）按 `archived_at ASC, updated_at DESC` 取第一条；
- `SetArchived`（`:255-269`）；
- `Delete`（`:271-293`）只删项目行，会话靠 `ON DELETE SET NULL` 解绑，违反 R1；
- `ListProjectsOptions.IncludeArchived`（`:301-302`），`ListProjects` 默认加 `archived_at IS NULL`（`:322-323`）。

gateway：
- 路由 `projects.Post("/:id/archive", s.handleProjectArchive)`（`pkg/gateway/api_extra.go:149`）；
- `handleProjectArchive`（`:4969-4993`）；
- `projectRow` 输出 `"archived_at"`（`:4741`）；
- `handleProjectsList` 读 `archived` 查询参数（`:4764` 一带）；
- `handleProjectsCreate`（`:4781` 起）用 `memory.ProjectKey(root)`（`:4815`）；
- `handleProjectDelete`（`:4995-5016`）。

测试路由镜像在 `pkg/gateway/api_extra_test.go:954-959`。

前端归档：
- `frontend/src/lib/api.ts:589`（`archivedAt`）、`:1875`（`archived` 参数）、`:1910-1911`（`projectArchive`）；
- `frontend/src/views/ProjectsView.vue:16`、`:87`、`:95`、`:108`、`:147`、`:157`、`:222`；
- `frontend/src/components/project/ProjectOverview.vue:17`、`:100`、`:192`；
- 文案键在 `frontend/src/locales/index.ts`（`grep -n "projects\.\(archive\|restore\|filterArchived\|fieldArchiv\)" frontend/src/locales/index.ts`）。

### 项目键

- `memory.ProjectKey(path)`（`pkg/memory/scope.go:299-312`）= `ProjectRoot(path)` 的路径分隔符与冒号换成 `-`。
- `ProjectRoot`（`:229-253`）向上找 `.git`，并把 git worktree 映射到主 worktree。
- 包依赖：`pkg/state` 只依赖 `pkg/config`、`pkg/llm`；`pkg/safety` 依赖 `home/config/llm/telemetry`，不依赖 `pkg/state`。所以 `pkg/state` 可以 import `pkg/safety` 用 `CanonicalPath`（`pkg/safety/engine.go:1080-1094`），没有环。改了 import 要重新生成依赖图。

### 本机数据（只读查询，2026-10-11）

- `user_version = 8`，`fb_projects` 0 行。
- 普通会话 305 个、无项目。
- `cwd` 为空 5 个（全是普通会话，无父会话）。
- 子会话 110 个：70 个 `source='subagent'`，40 个 `source=''` 的 fork（39 个 `origin='migrated'`）。全部有父会话，`cwd` 与父会话相同。
- 不同 `cwd` 13 个，其中 2 个是 forebrain-harness 仓库的子目录（这 2 个会话没有计划文件，项目键变化不需要搬文件）。
- `fb_cron_jobs` 0 行；`source='workshop'` 0 个。

## Commands you will need

| 用途 | 命令 | 成功预期 |
|---|---|---|
| Go | `CGO_ENABLED=1 go test -tags fts5 ./pkg/state/... ./pkg/gateway/... ./pkg/memory/... ./pkg/architecture/... -count=1` | 全部 `ok` |
| 全量构建 | `CGO_ENABLED=1 go build -tags fts5 ./...` | 无输出 |
| vet / fmt | `CGO_ENABLED=1 go vet -tags fts5 ./pkg/state/... ./pkg/gateway/...`；`gofmt -l pkg` | 无输出 |
| 依赖图 | `scripts/package-graph.sh` 后 `git diff --stat pkg/architecture/testdata/graph.json` | 只多出 `pkg/state → pkg/safety` 一条边 |
| 前端 | `cd frontend && pnpm test && npx vue-tsc --noEmit -p tsconfig.json` | 全绿 / 无输出 |
| 构建 | `make ui` | 成功 |

## Scope

**In scope**：
- `pkg/state/schema.sql`、`pkg/state/schema_migrations.go`、`pkg/state/project_store.go` 及其测试；
- `pkg/gateway/api_extra.go`（项目路由与处理函数）、`pkg/gateway/api_extra_test.go`；
- 前端归档相关的 `api.ts`、`ProjectsView.vue`、`ProjectOverview.vue`、locale 键，以及 `pkg/gateway/dist`；
- `pkg/architecture/testdata/graph.json`。

**Out of scope**：
- 新建项目、删除项目确认框等 web 界面改版（web 计划按设计稿做）；
- TUI 启动登记项目（028）；
- 运行时改用新项目键的调用方（`pkg/process/open.go:312,355` 等，027/028 处理），以及 `/migrate` 导入里的 `memory.ProjectRoot` 匹配；
- 定时任务、通道的项目化（035）。

## Git workflow

不建分支、不 commit、不 push、不 stash。

## Steps

### Step 1：项目键按项目根本身

在 `pkg/state/project_store.go` 新增：

```go
// ProjectKeyForRoot is a project's memory and plan-file identity: the project
// root itself, canonical, with separators and colons turned into dashes. It
// never walks up to a git root — a subdirectory is its own project (owner
// 2026-10-11).
func ProjectKeyForRoot(root string) string
```

- 先 `safety.CanonicalPath(root)`，出错就用 `filepath.Clean(strings.TrimSpace(root))`；
- 再做与 `memory.ProjectKey` 相同的字符映射（`/`、`\`、`:` → `-`，`filepath.ToSlash` 之后）；
- `handleProjectsCreate` 的 `memory.ProjectKey(root)`（`api_extra.go:4815`）改用它。

**Verify**：新单测 `TestProjectKeyForRootDoesNotWalkToGitRoot`。在临时目录 `git init` 一个仓库，建子目录 `sub`，断言 `ProjectKeyForRoot(repo/sub)` ≠ `ProjectKeyForRoot(repo)`，且只是 `repo/sub` 的映射。

### Step 2：schema.sql 变成终态

1. 删掉 `fb_projects.archived_at` 列，改写表注释（不再有"archiving is a soft delete"）。
2. `CREATE INDEX idx_fb_projects_agent_root ...` 改为 `CREATE UNIQUE INDEX idx_fb_projects_agent_root_unique ON fb_projects(agent_id, root);`。
3. `migrateV1Projects`（`schema_migrations.go:789-826`）去掉 `archived_at` 的读取和写入。旧库里已归档的项目照样复制成普通项目，在函数注释里写明。

### Step 3：迁移 vN+1 `migrateStateV<N>ToV<N+1>`

加进 `stateSchemaMigrations`，`stateSchemaVersion` 加 1。函数按下面的顺序执行，每一步都要对"当前 schema 建出的库"幂等：

1. **去掉归档**：`pragma_table_info('fb_projects')` 里还有 `archived_at` 就执行 `ALTER TABLE fb_projects DROP COLUMN archived_at`。若 SQLite 报错（版本过低或列被引用）→ STOP。
2. **合并重复根**：按 `(agent_id, root)` 分组，每组保留 `created_at` 最早的一条，其余各条的会话和定时任务改指向保留的那条，再删掉。然后 `DROP INDEX IF EXISTS idx_fb_projects_agent_root`，`CREATE UNIQUE INDEX IF NOT EXISTS idx_fb_projects_agent_root_unique ...`。
3. **找出要删除的会话**（建临时表 `temp.doomed(id TEXT PRIMARY KEY)`）：
   - `project_id IS NULL AND source <> 'workshop' AND TRIM(cwd) = ''` 的会话；
   - 再反复补入父会话已经在 `doomed` 里的子会话，直到没有新增；
   - 再补入父会话不存在的子会话（`parent_session_id IS NOT NULL` 但查不到父会话）。
4. **显式删除 `doomed` 的数据**（外键已关闭，不会级联），顺序为：
   1. `fb_run_waits`（`run_id IN (SELECT id FROM fb_runs WHERE session_id IN doomed)`）
   2. `fb_session_events`
   3. `fb_runs`
   4. `fb_actions`
   5. `fb_files`
   6. `fb_messages`
   7. `fb_session_ui_state`、`fb_session_model_state`、`fb_session_prompt_state`
   8. `fb_heartbeats`
   9. `fb_memory_stage1_outputs`（`thread_id IN doomed`）
   10. `fb_memory_jobs`（`kind='memory_stage1' AND job_key IN doomed`）
   11. `fb_cron_runs`（`session_id IN doomed`）
   12. `fb_sessions`

   动手前用 `grep -n "REFERENCES fb_sessions\|REFERENCES fb_runs\|REFERENCES fb_actions" pkg/state/schema.sql` 核对：若有表不在上面的列表里，补进去并在报告写明。
5. **归入项目**：对剩下的 `project_id IS NULL AND source <> 'workshop'` 的会话：
   - 先处理顶层会话（`parent_session_id IS NULL`）：在 Go 里按 `(agent_id, 规范化 cwd)` 分组。规范化用 `safety.CanonicalPath`，失败就用 `filepath.Clean` 后的原值，目录不存在也照样处理；
   - 每组按根找项目，找不到就插入一行：
     - `name` = 根的最后一段（根是 `/` 时用 `/`）；
     - `project_key = ProjectKeyForRoot(root)`；
     - 其余字段与 `ProjectStore.Create` 相同（`memory_scope='shared'`、`resource_access=1`、`pinned=0`、`created_at`/`updated_at` 取该组最早会话的 `created_at`）；
   - 绑定会话：`project_id` = 该项目，`cwd` 改成规范化后的根；
   - 再处理子会话：`project_id` 与 `cwd` 一律取父会话的值（父会话已经在上一步处理过）。
6. **定时任务**：删除 `project_id IS NULL` 的 `fb_cron_jobs` 和它们的 `fb_cron_runs`（本机 0 行）。
7. `slog.Info` 记录：删除的会话数、新建的项目数、归入的会话数、删除的定时任务数。
8. 回收交给框架末尾的 VACUUM，本步不做。

`source='workshop'` 的会话一律不动（主代理级工坊会话没有项目是设计上的例外）。

**Verify**：`go build ./pkg/state/...`；Step 6 的迁移测试。

### Step 4：项目存储的新语义

`pkg/state/project_store.go`：
1. 删掉 `ArchivedAt`、`SetArchived`、`IncludeArchived`，从 `projectColumns`、`scanProject`、`Create` 里去掉该列。`GetByRoot` 只按 `agent_id, root` 精确查（唯一）。
2. `Create`：插入撞上唯一索引时（`sqlite3.ErrConstraintUnique`，用 `errors.As` 判断，仓库里找现成写法：`grep -rn "ErrConstraintUnique" pkg`）返回新错误 `ErrProjectRootTaken`，同时带回已存在的项目。签名改为 `(Project, error)`，冲突时 Project 是已有的那条。
3. 新增 `FindOrCreateByRoot(ctx, root string, defaults CreateProjectInput) (Project, bool /*created*/, error)`：
   - 根用 `safety.CanonicalPath` 规范化；
   - `ProjectKey` 为空时取 `ProjectKeyForRoot`；
   - 先 `GetByRoot`，没有再 `INSERT ... ON CONFLICT(agent_id, root) DO NOTHING`，然后再读一次。这样 TUI 与 web 同时登记同一目录时也只有一条。

   028（TUI）与 029（web 选文件夹）都调用它。
4. `Delete(ctx, id)` 在一个事务里：
   1. 该项目的会话（含子会话）有 `fb_runs.status IN ('running','waiting_action')` 的 run → 返回新错误 `ErrProjectBusy`，不删；
   2. 删除该项目的定时任务及其运行记录；
   3. 删除该项目的全部会话：运行时外键是开着的（核对：`grep -n "foreign_keys" pkg/state/db.go`，DSN 若没开 → STOP），消息、事件、run 等会级联；另删 `fb_memory_jobs` 的 `memory_stage1` 行与 `fb_cron_runs` 行；
   4. 删除项目行。
5. 新增 `DeletePreview(ctx, id) (sessions int, cronJobs int, running bool, err error)`，供确认框显示数量。会话数不含子会话。

### Step 5：gateway 与前端去掉归档、接上新的删除

1. 删除 `/:id/archive` 路由与 `handleProjectArchive`（源码与 `api_extra_test.go` 的路由镜像都删）；`projectRow` 去掉 `archived_at`；`handleProjectsList` 去掉 `archived` 参数。
2. `handleProjectsCreate`：`ErrProjectRootTaken` → 409，body 为 `{"error":"project_root_taken","project": projectRow(已有)}`。
3. `handleProjectDelete`：
   - `ErrProjectBusy` → 409 `{"error":"project_busy"}`；
   - 未找到 → 404；
   - 成功后照旧 `RebindAgent`。
4. 新路由 `GET /:id/delete-preview` → `{"sessions":n,"cron_jobs":m,"running":bool}`。
5. 前端：
   - `api.ts` 删 `archivedAt`、`archived` 参数、`projectArchive`，加 `projectDeletePreview`；
   - `ProjectsView.vue`、`ProjectOverview.vue` 删掉归档按钮、"已归档"筛选与标记，以及对应的 locale 键（两种语言）。
   - 删除按钮沿用今天的确认方式；按设计稿改成弹窗是 web 计划的事。
   - 删除返回 409 `project_busy` 时显示 locale 文案"项目里有对话正在运行，等它结束后再删除"（新键，两种语言）。
6. `make ui`。

### Step 6：测试

`pkg/state/schema_migrations_test.go` 新增 `TestStateV<N>UpgradesToV<N+1>GivesEverySessionAProject`。照 `TestStateV7UpgradesToV8...`（`:1655`）造库：用当前 schema 建库，补回 `archived_at` 列与非唯一索引，`user_version` 调回 N，插入下列会话：

| 会话 | 预期 |
|---|---|
| A、B：同一 cwd，其中 B 的 cwd 带尾部 `/` | 同一个新项目，cwd 规范化 |
| C：cwd 是不存在的目录 | 照样建项目 |
| D：cwd 为空，带消息、事件、run、动作、文件、记忆任务 | 全部删除，无孤儿行 |
| E：D 的子会话 | 删除 |
| F：A 的子会话（subagent）、G：A 的 fork（`source=''`、有父会话） | 跟 A 同一个项目，cwd 同 A |
| W：`source='workshop'`、无项目、cwd 在临时 home 内 | 不动 |
| P1、P2：两个重复根的项目，P2 绑有会话和定时任务 | 合并到较早的 P1，P2 删除 |
| 一条已归档项目 | 变成普通项目 |
| `project_id IS NULL` 的定时任务及其运行记录 | 删除 |

断言：
- `user_version` = N+1；
- `PRAGMA foreign_key_check` 为空；
- 唯一索引存在；
- `archived_at` 列不存在；
- 再跑一次迁移函数（幂等）不报错、数据不变。

另外：
- `project_store_test.go`：
  - `FindOrCreateByRoot` 并发两次同一根只有一条；
  - `Create` 撞根返回 `ErrProjectRootTaken` 和已有项目；
  - `Delete` 在有 running run 时返回 `ErrProjectBusy`，在空闲时删掉会话（含子会话）、消息、定时任务；
  - `DeletePreview` 数量正确。
- gateway：
  - 归档路由 404；
  - 删除忙 409；
  - 创建撞根 409 带已有项目；
  - `delete-preview` 返回数量。
- 若已有测试断言归档行为（`grep -rn "Archived\|archive" pkg/state/*_test.go pkg/gateway/*_test.go frontend/src --include='*.test.ts'`），删除或改写为新语义。

### Step 7：本机数据的干跑（只读副本）

1. `cp ~/.forebrain/state/forebrain.state.sqlite $TMPDIR/fb025.sqlite`。复制前确认没有进程在写：先 `sqlite3 -readonly <原库> "PRAGMA wal_checkpoint(PASSIVE);"`，或者用 `.backup` 命令复制。
2. 在副本上运行迁移：写一个只在测试里用的小入口，或者用 `go test` 里一个读环境变量 `FB025_DB` 的跳过式测试，打开副本即可迁移。
3. 核对：新建项目 13 个、归入 415 个会话（305 个顶层会话 + 110 个子会话）、删除 5 个、`foreign_key_check` 为空、迁移后 `freelist_count = 0`。
4. 把这些数字写进报告。删除副本。

数字对不上 → STOP，报告差异，不要改迁移去迎合。

## Test plan

见 Step 6、7。

## Done criteria

- [ ] Commands 表全部通过；`make ui` 成功
- [ ] `grep -rn "archived_at\|SetArchived\|IncludeArchived\|projectArchive\|ArchivedAt" pkg frontend/src --include='*.go' --include='*.ts' --include='*.vue' | grep -v schema_migrations` 无输出
- [ ] `grep -n 'UNIQUE INDEX idx_fb_projects_agent_root_unique' pkg/state/schema.sql` 有 1 行
- [ ] 迁移测试与 Step 7 本机副本干跑的数字都写进报告
- [ ] `git status --short` 中本计划新增的改动只涉及 Scope 列出的文件

## STOP conditions

1. Drift check 不符。
2. `ALTER TABLE ... DROP COLUMN` 不可用。
3. 运行时连接没有开外键（`Delete` 的级联就不成立）。
4. Step 3 第 4 步核对时，发现有引用会话的表不在列表里，而且它的语义不清楚该删还是该保留。
5. Step 7 的数字与本计划写的本机数据不符。

## Maintenance notes

- 以后新增引用 `fb_sessions` 的表，要同时加进本迁移第 4 步的删除列表（迁移时外键关闭）和 `ProjectStore.Delete` 的事务。迁移测试里的"无孤儿行"断言会兜住遗漏。
- `ProjectKeyForRoot` 是项目键的唯一来源。027/028 会把运行时的调用方（`process/open.go`、TUI 的 `memory.ProjectRoot` 调用）切换过来；在那之前，TUI 在仓库子目录里启动时用的仍是旧键。

---

## 勘误（2026-10-11，执行后由评审方补写）

1. **Step 1 不能用 `pkg/safety`**：计划让 `pkg/state` import `pkg/safety` 用 `CanonicalPath`，并预期
   `graph.json` 只多一条 `pkg/state → pkg/safety` 边。实际不可能：`pkg/architecture/cache_test.go:357` 的
   `packageLayer` 把 state 定为 layer 1、safety 定为 layer 2，`TestLayerCeiling`（:371）禁止低层 import 高层。
   已落地实现改为包内等价的 `canonicalStatePath`（`pkg/state/project_store.go`），**graph.json 不新增该边**。
   不要再按原计划改回 `pkg/safety`。
2. **Step 7 的预期数字已过时**：真实库在执行日已长到 444 会话 / 14 个去重根，实测
   `doomed=5 deleted=5 created_projects=14 adopted=439 deleted_cron_jobs=0`（评审方独立复跑一致）。
   计划里写的 13 项目 / 归入 415 是撰写时的快照，不是缺陷。
3. 迁移耗时：3.7GB 的库上约 **217 秒**（含末尾 VACUUM）。
