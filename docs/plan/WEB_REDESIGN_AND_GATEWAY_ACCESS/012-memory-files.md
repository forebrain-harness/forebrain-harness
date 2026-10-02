# 计划 012：记忆文件管理——主代理与项目维度的分页列表、搜索、编辑、删除与重置

> **执行者须知**：逐步执行，每一步都运行"验证"命令并确认结果后再进入下一步。出现"STOP 条件"中的任何情况，立即停止并报告。
> 完成后更新 README 中本计划的状态行。**不要提交代码。**
>
> **漂移检查（先运行）**：
> `git diff --stat b92a1b1..HEAD -- pkg/memory/ pkg/gateway/memories_api.go pkg/gateway/memories_api_test.go frontend/src/lib/api.ts frontend/src/views/MemoriesView.vue frontend/src/components/project/`
> 计划 008 建过项目空间壳（含 memory 占位标签）。那是预期的。`memories_api.go`、`memories_api_test.go`、`api.ts` 应无漂移——它们出现在这里是因为本计划要扩展 `handleMemoriesReset`，有输出即先按"现状"对照。

## 状态

- **优先级**：P1
- **工作量**：L
- **风险**：MED（新端点全部要 `ResolveWithinRoots` 级别的越界防护；核心文件保护与清空语义要小心）
- **依赖**：计划 007（`/memories` 菜单位）、计划 008（项目空间 memory 标签挂点）
- **类别**：direction / feature
- **基线**：提交 `b92a1b1`，2026-09-29

## 为什么要做

owner 要求（第 33 条）："主代理维度和项目维度的记忆页面必须展示记忆文件分页列表，支持分页查询、按创建或更新时间升降序、记忆内容关键词搜索、单条查看和编辑、单条删除和多选批量删除以及重置/清空全部"；并明确"必须可以让用户编辑和修改记忆文件，删掉或者修改错误的或者过时的记忆"。预览 captions 的 `memories` 条（405 行）是页面合同：核心文件 MEMORY.md、memory_summary.md 只能编辑或清空，不能删除；项目空间的记忆标签是同一套界面，只列本项目的。

清空/重置的端点形态由 owner 于 2026-09-29 复核时拍板（决策 D10）：**统一收口为既有 `POST /api/memories/reset` 一个端点**，不新增独立的 clear 端点。

## 现状

- **引擎能力**（`pkg/memory/backend.go`）：`Backend` 按 scope 路由（`route :78`，`global/` 前缀 = global scope，其余 = project scope）。`List :277`（`ListRequest :119`：`Path` 前缀 + `Cursor` + `MaxResults`，游标分页，**无排序/搜索过滤**；`Entry :114` 只有 `Path/EntryType`，无时间戳）；`Read :364`（行号分页读文本，拒绝 symlink/非常规文件）；`Search :435`（FTS，`SearchRequest :146`：`Queries []string` + `Path` 前缀 + `TopK`，`SearchMatch` 返回命中行）；FTS 索引按 scope 隔离（`ftsindex.go:31`）且**读时同步**（搜索前 sync，写文件后无需重建）。
- **清空与重置**：`memory.Clear(root)`（`control.go:54`）清一个 scope 的记忆目录与状态目录；`memory.Store.Reset/ResetScope`（`store.go:374/:401`）只清状态库行（stage-1 输出与作业），不清文件。两者组合才是完整重置。
- **HTTP 层**：`pkg/gateway/memories_api.go`——`GET/POST /api/memories/settings`（`:21`，三个开关）、`POST /api/memories/reset`（`handleMemoriesReset :85`：查询参数 `?scope=all` 清本主代理全部 scope（`Store.Reset` + `Clear` global + `Clear` 每个项目 scope），缺省清调用会话的项目 scope（取 `s.Runner.ProjectKey :123`，为空 400 提示传 `?scope=all`）；响应 `{"ok":true}`）。**没有文件列表/读写/删除端点**。reset 的全部调用方都在仓库内：前端 `frontend/src/lib/api.ts:1185`（POST 空 body，即缺省分支）与 `pkg/gateway/memories_api_test.go` 的两个既有用例（`:19`、`:39`）；TUI/CLI/渠道不调它。
- **scope 解析**：`memory.ResolveRootsForAgent(workspaceRoot)`（`scope.go:132`）→ `Roots` → `Roots.Scope(memory.GlobalScope())` 或 `Scope(projectKey)`（`scope.go:146`；`ProjectScope(key)` 空键返回 false，`Root.MemoryRoot` 是 `scope.go:111` 的目录根）。项目键 = 项目空间的 `ProjectKey`（`pkg/state/project_store.go:35`）。
- **记忆目录是 git 仓库**：`pkg/memory/workspace.go:235` `runGit`；巩固流程以 git 为 baseline（`resetGitBaseline :47`）。UI 的文件编辑**不需要**每次 commit（git 是巩固管线的事实，不是 UI 的）；如实现 commit 则复用 `runGit`——本计划选择不 commit，保持 UI 写入与巩固 baseline 语义解耦。
- **前端**：`frontend/src/views/MemoriesView.vue`（142 行）是简单只读列表；项目空间 memory 标签是 008 占位。
- README 全局规则（90-115 行）全部适用。

## 设计

### 1. 文件管理 API（新建 `pkg/gateway/memories_files_api.go`）

所有端点带 `scope` 参数定位记忆根：`scope=global`（主代理全局）或 `scope=project&project_id=<id>`（校验项目属于当前主代理后取其 `ProjectKey`；`ProjectKey` 为空的项目没有独立记忆 scope，返回 400 并说明）。端点内部 `ResolveRootsForAgent(当前主代理工作区)` → `Roots.Scope(...)` → `Root.MemoryRoot`。

| 端点 | 语义 |
| --- | --- |
| `GET /api/memories/files?scope=...&page=<n>&page_size=<20>&sort=<created\|updated>&order=<asc\|desc>&q=<关键词>` | 分页文件列表：`{total, page, page_size, files:[{path(相对 MemoryRoot、正斜杠), size_bytes, created_at, updated_at, core}]}`。目录递归枚举 `MemoryRoot` 下全部常规文件（跳 symlink）；时间取 `os.Stat` 的 Ctime（macOS birthtime）/mtime——**创建时间取 birth time，不可得时等于 updated_at**；`q` 非空时先用 FTS（`Backend.Search`，path 聚合去重取匹配文件）筛出候选集合再进分页——FTS 未覆盖（二进制/超长）的文件不因搜索丢失：`q` 同时做一次文件名子串匹配并入候选。`core` = 相对路径为 `MEMORY.md` 或 `memory_summary.md`（global scope 根下）——**核心文件名只在这两处**，与引擎 `instruction.go:34` 一致 |
| `GET /api/memories/file?scope=...&path=<rel>` | 读全文（≤ 2 MiB）。`path` 解析必须在 `MemoryRoot` 内（`filepath.Clean` + 前缀校验 + `os.Lstat` 拒 symlink，与 `backend.read :380` 同一防线），越界 400 |
| `PUT /api/memories/file?scope=...&path=<rel>` | 覆写文本（≤ 2 MiB），同一防线；文件不存在时允许创建（`EntryType file` 白名单：只允许 `.md`/`.txt`/`.json` 新建——其他扩展名 400，防止把记忆目录当通用文件库） |
| `POST /api/memories/files/delete` `{scope, paths: [...]}` | 批量删除：`paths` 上限 100；`core` 文件拒删（403，逐条结果返回）；逐文件同一防线 |
| （清空/重置不在此表——统一走设计第 2 节的既有 `POST /api/memories/reset` 端点，见 owner 决策 D10） | |

写后一致性：FTS 读时同步，无需主动重建；页面在写/删后重拉列表即可。

### 2. 统一重置端点（扩展既有 `POST /api/memories/reset`，改 `pkg/gateway/memories_api.go`）

记忆的清空/重置只有这一个端点（owner 决策 D10：统一收口；原独立 `/api/memories/clear` 方案作废）。请求带可选 JSON body `{"scope": "...", "project_id": "..."}`（`io.LimitReader` 64 KiB + `DisallowUnknownFields`，与同文件 settings 端点同一风格；**空 body 等价 `{"scope":"session"}`**，现有前端调用不变）：

| scope | 语义 | 相对现状 |
| --- | --- | --- |
| `session`（缺省） | 清调用会话的项目 scope：`s.Runner.ProjectKey` 为空时 400 | 原样保留 |
| `all` | 清本主代理全部 scope：`Store.Reset` + `Clear(global)` + `Clear(每个项目 scope)` | 原样保留（取代 `?scope=all` 查询参数） |
| `global` | 只清 global：`Store.ResetScope(GlobalScope)`（`store.go:401` 的 global 分支清巩固作业）+ `memory.Clear(roots.Scope(GlobalScope()))` | **新增**，主代理记忆页"清空全部"用 |
| `project` | 清指定项目：必带 `project_id`；`projectStore.Get` 校验属于当前主代理（不属于 404）、`ProjectKey` 为空 400；`Store.ResetScope(ProjectScope(key))` + `Clear(...)` | **新增**，项目空间记忆标签"清空全部"用 |

- **查询参数 `?scope=all` 删除**，body 是唯一形态。仓库内调用方同步更新：`pkg/gateway/memories_api_test.go` 的 `TestMemoriesResetRequiresStoreAndLeavesFilesUntouched`（`:19`）与 `TestMemoriesResetClearsStoreAndFiles`（`:39`）改传 body；`frontend/src/lib/api.ts:1185` 发空 body 走缺省分支，调用不变。
- 响应统一为 `{"ok": true, "scope": "<session|all|global|project>"}`（新增 `scope` 字段供界面提示清了什么；旧调用方只读 `ok`，不受影响）。

### 3. 主代理记忆页（`/memories`）

按预览 `memories` 条重写 `MemoriesView.vue`：
- 工具栏：搜索框（关键词，防抖 300ms）、排序选择框（创建时间/更新时间 × 升/降序，默认更新时间降序）、分页器（page/page_size）、"清空全部"红色按钮（二次确认，文案说明"删除本主代理全部记忆文件与巩固状态，不可恢复"）。
- 表格：勾选框、记忆文件（相对路径）、内容摘要（首行截断 80 字符，来自列表时顺手读前 200 字节——可选优化，若实现需保持单请求 `page_size` 上限 50）、创建时间、更新时间、操作（查看/编辑、删除）。
- 批量条：勾选后浮现"删除所选（n）"。
- 编辑器：抽屉或弹层内 textarea（等宽），保存走 PUT；核心文件行显示锁形徽标 + 提示"核心文件只能编辑或清空，不能删除"。

### 4. 项目空间记忆标签（`/projects/:id/memory`）

替换 008 占位 `ProjectMemoryTab.vue`：与 `/memories` 同一套组件（抽公共 `components/memory/MemoryFilesPanel.vue`，props：`scope` 固定 project + `projectId`），`ProjectKey` 为空的项目显示"该项目未启用独立记忆作用域"空态。页头"项目"作用域徽标。

## 需要的命令

| 用途 | 命令 | 成功标志 |
| --- | --- | --- |
| Go 静态检查 | `go vet ./...` | 退出码 0 |
| Go 测试 | `CGO_ENABLED=1 go test -tags fts5 ./pkg/memory/... ./pkg/gateway/... -count=1` | 全部 `ok` |
| 前端单测 | `cd frontend && corepack pnpm test` | 全部通过 |
| 真机验证 | `scripts/acceptance/web_e2e.sh` | 最后一行 `web e2e: PASS` |
| 缓存不变式 | `git diff --stat -- pkg/agent pkg/assembly pkg/llm pkg/turn/prompt* internal/agentrun` | 无输出 |

## 范围

**只允许修改**：
- `pkg/gateway/memories_files_api.go`（新建）、`pkg/gateway/memories_files_api_test.go`（新建）、`pkg/gateway/api_extra.go`（仅路由注册行）
- `pkg/gateway/memories_api.go`（仅 `handleMemoriesReset`：加 body 解析与 `global`/`project` 两个分支、删 `?scope=` 查询参数、响应加 `scope` 字段）、`pkg/gateway/memories_api_test.go`（既有两个 reset 用例改传 body + 新增 global/project 用例）
- `frontend/src/views/MemoriesView.vue`（重写）、`frontend/src/components/memory/MemoryFilesPanel.vue`（新建）、`frontend/src/components/project/ProjectMemoryTab.vue`（替换 008 占位）
- `frontend/src/locales/index.ts`、`frontend/src/lib/api.ts`（reset 封装加可选 body 参数）
- `frontend/e2e/memory-files.spec.ts`（新建）

**不要碰**：
- `pkg/memory/**`（引擎只读复用；若发现必须改引擎——例如 birth time 需要引擎支持——走 STOP 条件）。
- `memories_api.go` 的 settings 端点；`handleMemoriesReset` 的 `session`/`all` 两个分支的既有语义（只换入参形态，不动清空逻辑）。
- 计划 009/010/013 的页面。

## 步骤

### 第 1 步：文件管理端点

实现设计第 1 条，`memories_files_api_test.go` 覆盖：分页总数与页切；`sort=updated desc` 首行是最近改动文件；`q` 命中文件内容（FTS）与文件名（子串）两个来源；核心文件删除 403、编辑 200；`path=../x` 400；symlink 拒绝；`.sh` 新建 400；其他主代理的 project_id 404；`ProjectKey` 为空 400。清空/重置的用例在第 2 步（统一端点）。
结构参照：gateway handler 测试照 `pkg/gateway/memories_api_test.go:39` `TestMemoriesResetClearsStoreAndFiles`（同文件既有记忆端点的写法），引擎侧行为照 `pkg/memory/backend_test.go` 的既有用例；越界与 symlink 用例照 `pkg/memory/control_test.go`。

**验证**：`CGO_ENABLED=1 go test -tags fts5 ./pkg/gateway/... -count=1` 全部 `ok`；`go vet ./...` 退出码 0。

### 第 2 步：统一重置端点

实现设计第 2 条：`handleMemoriesReset` 加 body 解析（空 body = `session`）、`global`/`project` 分支、删 `?scope=` 查询参数、响应加 `scope` 字段；`memories_api_test.go` 既有两个用例改传 body，并新增：`scope=global` 只清 global（项目 scope 文件仍在）；`scope=project` 清指定项目、他人项目 404、`ProjectKey` 为空 400；未知 scope 值 400；`project` 缺 `project_id` 400；空 body 行为与改动前完全一致（回归既有 `:39` 用例断言）。

**验证**：`CGO_ENABLED=1 go test -tags fts5 ./pkg/gateway/... -count=1` 全部 `ok`；`go vet ./...` 退出码 0。

### 第 3 步：两个页面

实现设计第 3、4 条（公共面板组件、双语）。"清空全部"按钮（主代理页）走 `{scope:"global"}`，项目空间标签的清空按钮走 `{scope:"project", project_id}`；确认弹窗文案按 scope 区分（"清空本主代理全部记忆" / "清空本项目的记忆"）。

**验证**：`cd frontend && corepack pnpm test` 全部通过。

### 第 4 步：真机验证

新建 `frontend/e2e/memory-files.spec.ts`：
1. `/memories`：列表出现（脚手架 home 的 global scope 至少有 MEMORY.md 等种子文件）；核心行有锁形徽标、无删除按钮；截图 `memories-list.png`。
2. 排序与分页：造 25 个文件（e2e 脚手架直接写文件），`page_size=20` 翻页出现第 2 页；改排序首行变化。
3. 搜索：输入某个只在文件正文出现的词，结果含该文件；输入只匹配文件名的词同样命中。
4. 编辑：打开 MEMORY.md、追加一行、保存、重新打开一致；再编辑一个普通文件并验证。
5. 删除：新建 `notes/e2e-temp.md`（PUT 允许 .md 新建）后删除成功；尝试批量删除含 MEMORY.md 的选择——MEMORY.md 拒绝且其余删除成功（逐条结果）。
6. 清空：项目空间记忆标签点"清空全部"（`{scope:"project"}`）→ 该项目列表为空，主代理页 global 文件仍在；`/memories` 页点"清空全部"（`{scope:"global"}`）→ global 列表为空，刚清过的项目文件不受影响；随后的记忆搜索（走既有 FTS）无结果。
7. 项目空间 memory 标签：只列该项目 scope 的文件（写入一个项目记忆文件后出现在列表）；另一项目的文件不出现（隔离断言）；截图 `memories-project.png`。

**验证**：`scripts/acceptance/web_e2e.sh` → 最后一行 `web e2e: PASS`。

## 完成标准（全部满足）

- [x] 第 1-3 步的验证全部通过
- [x] `scripts/acceptance/web_e2e.sh` 最后一行 `web e2e: PASS`（58/58，2.3 分钟）
- [x] 核心文件在两层页面都不可删除（e2e 断言）
- [x] 越界/符号链接/扩展名白名单的安全用例全部有自动化测试且通过
- [x] `git status --short pkg/gateway/dist` 无输出；缓存不变式检查无输出
- [x] README 状态行已更新

## STOP 条件

- "现状"摘录与代码对不上（尤其 `Backend.route` 的 scope 前缀语义、`Clear` 的目录范围、`instruction.go` 的核心文件名）。
- 创建时间无法可靠取得（`os.Stat` birth time 在目标平台不可用且引擎无替代字段）——与 owner 确认是否退化为只按更新时间排序。
- 清空/删除需要改 `pkg/memory` 引擎（例如 `Clear` 不覆盖某类产物）。
- 需要修改"范围"之外的文件。

## 维护说明

- 核心文件保护名单（`MEMORY.md`、`memory_summary.md`）与引擎 `instruction.go` 是一处事实两端引用；引擎改名时本 API 的 `core` 判定与 403 名单同步。
- 记忆目录的 git baseline 属于巩固管线：UI 写入不 commit 是有意决策，若未来 UI 要提供"查看历史"，走 `runGit` 只读实现，另立计划。
- **`POST /api/memories/reset` 是记忆清空/重置的唯一入口**（D10）：scope 的合法值集合 `session|all|global|project` 以 handler 的白名单为准；新增记忆面端点沿用"显式 scope 参数"形态，不要再派生别的清空端点。`?scope=` 查询参数已删，外部调用方一律走 body。
- `Store.Reset/ResetScope`（清状态库行）与 `memory.Clear`（清文件目录）必须成对调用——只调其一会出现"列表空了但巩固状态还在"（或反之）的半清状态；现有 `session`/`all` 分支即是这个模式。
