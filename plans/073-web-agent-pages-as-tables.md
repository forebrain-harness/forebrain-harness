# Plan 073：菜单栏主代理页面改成“无标题 + 工具栏 + 表格 + 分页 + 表下说明”，对应接口改服务端分页（项目、规则指令、技能、子代理、模型服务、工具、权限、记忆）

> **执行者须知**：按步骤顺序执行，每步跑验证命令并确认预期结果后再进下一步。触发
> "STOP conditions" 时停下报告，不要即兴发挥。**不要更新 `plans/README.md`**（评审方维护索引）。
> **禁止提交代码**（owner 手动提交）：不建分支、不建 worktree、不 commit、不 push、不 stash；改动留在当前工作区。
> 先跑 `scripts/fetch-libtokenizers.sh`；`go test` 报 pdfium 找不到时先 `make pdfium pdfium-fonts` 并照 Makefile 导出 `PKG_CONFIG_PATH`。
>
> **前置**：plan 071 已完成（`pkg/gateway/list_page.go`、`frontend/src/components/common/TablePager.vue` 等存在）；plan 072 建议先完成（不强制）。
> **Drift check（先跑）**：以下每条都必须各有输出，否则 STOP：
> ```
> test -f pkg/gateway/list_page.go && test -f frontend/src/composables/useServerList.ts && echo ok
> grep -n 'order := "pinned DESC, updated_at DESC, id"' pkg/state/project_store.go
> grep -n '_ = json.NewEncoder(w).Encode(s.Env.Tools().ToolMetas())' pkg/gateway/api_extra.go
> grep -n '"installed": s.decorateSkillList(r.Context(), launch, projectID, svc, overview.Installed),' pkg/gateway/api_extra.go
> grep -n '<PageTopRow :title="t(.projects.title.)"' frontend/src/views/ProjectsView.vue
> grep -n "{{ t('skills.detail') }}" frontend/src/components/skills/SkillsTable.vue
> ```

## Status

- **Priority**: P1
- **Effort**: L
- **Risk**: MED（8 个页面 + 7 个接口加分页；接口保持“不带 page 时原样回答”，非表格调用方不受影响）
- **Depends on**: plans/071-web-list-foundation.md
- **Category**: direction（设计稿落地）
- **Planned at**: commit `4888d76` + 工作区，2026-10-18

## Why this matters

owner 2026-10-17/18 把菜单栏里主代理的页面（项目、规则指令、技能、子代理、模型服务、工具、权限、记忆）统一成同一套：
**内容里没有标题**（顶栏已写页面名）；表格上方一行工具栏，**左边搜索框**（必有）+ 筛选，**右边**原来标题行的按钮；
表格固定列宽、每格一行、长内容截断、悬停看全文；**操作栏全是同尺寸图标按钮**（悬停出提示）；可排序的列在**表头**排序；
表格下方**分页器**（每个表格都有），分页器下方一段**说明**（原页面描述 + 原有提示合成一段）。
owner 2026-10-18 裁决：**所有列表服务端分页**（搜索/筛选/排序都在服务端做完再切页，带总数）；原型没画的现有功能**保留**并按同一套规则放；
原型需要而后端没有的列**后端补字段**。现状：这些页面都还是“标题行 + 卡片/列表 + 文字按钮”，接口一次返回全量。

## 设计稿（验收依据，实施须 1:1 还原）

- 原型 https://claude.ai/artifact/KKfPW8dXWScuRdTzonsk2F（本地 `docs/plan/WEB_REDESIGN_AND_GATEWAY_ACCESS/app-preview.html`），点菜单栏各项即可看到每页；
  另有状态按钮 **项目页**、**工具页**、**工具 · 查看入参**、**分页 · 多页（记忆）**、**记忆 · 表头排序**、**技能页**、**规则指令**、**模型服务**、**权限**。
- 每页对应的原型代码（实现照它的列、宽度、文案、图标）：
  | 页面 | 原型代码 |
  |---|---|
  | 项目 | `pages.projects` |
  | 规则指令 | `pages.rules` → `rulesBlock('agent', …)` |
  | 技能 | `pages.skills` → `skillsBlock('agent', …)`、`srcBadge` |
  | 子代理 | `pages.subagents` |
  | 模型服务 | `pages.providers` |
  | 工具 | `pages.tools` |
  | 权限 | `pages.permissions` → `ruleTable(agentRules, 'agent')` |
  | 记忆 | `pages.memories` → `memBlock('agent', …)`、`sortTh` |
  共用件（plan 071 已做）：`ListToolbar`（= `tabTools`）、`ListNote`（= `tabNote`）、`TablePager`（= `pager`）、`SortHeader`（= `sortTh`）、
  `RowAction/RowActions/RowActionSlot`（= `act/rowActs/actSlot`）、`v-tip`、表格类 `forebrain-table--fixed`、`forebrain-cell-1`。

### 每页的目标结构（从原型逐页抄出；“+”表示原型没有、按 owner 裁决保留的现有功能）

**1. 项目**（`ProjectsView.vue`）
- 工具栏：左 搜索框 `搜索名称或路径`；右 `新建项目`（主色，`+` 图标）、`刷新`（`rotate-cw` 图标）。
- 表格 `forebrain-table--fixed`，列宽：项目 210px｜路径 自适应｜说明 200px｜对话 64px（右对齐、等宽数字）｜更新时间 150px｜操作 112px。
  - 项目：名称是链接（点进项目空间），名称前保留项目图标（+，emoji，有才显示）；名称后徽标：`置顶`（品牌色）、`未信任`（灰，`trustLevel !== 'trusted'`）、`目录不存在`（红，`missing`）、`仅本项目记忆`（+，灰，`memoryScope === 'project_only'`）。
  - 路径：等宽、灰、一行截断、悬停全文；说明：一行截断、悬停全文；对话：该项目的对话数（**后端新字段 `session_count`**）；更新时间 `YYYY-MM-DD HH:mm`。
  - **表头排序**：`项目` 列（首次 A→Z）与 `更新时间` 列（首次 新→旧）；默认 更新时间 新→旧；**置顶的始终在最上面**（服务端排序时先按 pinned）。
  - 操作：`进入项目空间`（`folder-open`）、`置顶`/`取消置顶`（`pin`/`pin-off`，点了立即生效并重新加载当前页）、`删除`（`trash-2`，红；沿用现有“预览 + 输入项目名确认”的删除弹窗）。
- 分页器；说明：`每个项目有独立的项目空间，项目的配置和数据都在那里查看和修改。`

**2. 规则指令**（`RulesView.vue`）
- 工具栏：左 搜索框 `搜索规则文件`；右 `新建规则文件`（主色）、`刷新`（+）。
- 表格：文件（等宽）｜作用（灰）｜更新时间｜操作 64px：`编辑`（`pencil`）。
- 分页器；说明：`本主代理的规则文件，在主代理工作区根目录，每个会话开头注入；保存后新会话生效。`

**3. 技能**（`SkillsView.vue` + 共用 `components/skills/SkillsTable.vue`）
- 工具栏：左 搜索框 `搜索技能` + 来源筛选（**只列该页实际会出现的来源**：主代理技能页 = `全部来源/主代理/共享/内置`；项目技能 = `全部来源/项目/主代理/共享/内置`；共享技能库 = `全部来源/共享/内置`）；
  右 （选中时）`已选 N 个`、`下载所选`（`download` 图标）、`离线安装`（`upload`）、`在线安装`（`globe`）、`发现并安装`（`search`）、`新建技能`（主色 `plus`）、`刷新`。共享库没有“发现并安装”“新建技能”。
- 表格：勾选列 30px｜技能（等宽名称 + 徽标 `有未发布的修改`/`有更新`，下一行灰字描述 · 目录）｜来源（`主代理/项目/共享/内置` 徽标，**只写层级名、不带主代理名**）｜状态（右对齐：草稿徽标，或 `vN` 徽标 + 本层可改时开关 / 继承来的显示 `已启用`/`未启用` 徽标 + 锁图标）｜操作 110px：
  `查看`（`eye`，原“详情”按钮就是它；本层可改的行或有更新的行才有，否则空位）、`下载 zip`（`download`）、`删除`（`trash-2` 红；本层可改且非内置才有，否则空位）。
- 分页器；说明 = 原页面描述 + 原表下提示合成一段。

**4. 子代理**（`SubagentsView.vue`）
- 工具栏：左 搜索框 `搜索子代理或任务`；右 `全部停止`（红字）、`刷新`。
- 表格：子代理（等宽 label）｜任务（`title`，截断）｜所在会话（会话标题链接，点开该会话；**后端补 `session_title`**）｜状态（+，徽标）｜已运行（`mm:ss`，由开始时间算）｜操作：`打开会话`（+，`message-square`）、`停止`（`circle-stop`，红）。
- 分页器；说明：`本主代理所有会话里正在运行的子代理。`

**5. 模型服务**（`ProvidersView.vue`）
- 工具栏：左 搜索框 `搜索服务商或模型`；右 `添加模型服务`（主色）、`刷新`。
- 表格：顺序 70px（`主用` 品牌徽标 / `回退 N` 灰徽标）｜服务商｜模型（等宽，多个用 `、`）｜Base URL（等宽灰）｜API Key（`已保存 ••••xxxx` 绿徽标 / `未设置` 黄徽标）｜操作 110px：
  `上移`（`arrow-up`；第一条为空位）、`编辑`（`pencil`）、`删除`（`trash-2` 红）。上移按**全局顺序**移动（不是页内）。
- 分页器；说明：`按顺序尝试：第一条主用，其余回退。`

**6. 工具**（`ToolsView.vue`）
- 工具栏：左 搜索框 `搜索工具名或说明` + `全部分类` 下拉 + `全部类型`（`只读/有副作用`）下拉；右 `刷新`。
- 表格 `forebrain-table--fixed`：工具 280px（等宽，截断）｜说明（截断）｜分类 136px（中文名：`filesystem→文件`、`shell→命令`、`code→代码`、`web→网络`、`memory→记忆`、`session→会话`、`permissions→权限`、`skill→技能`、`mcp→MCP`，其它原样）｜类型 150px（`只读` 绿 / `有副作用` 黄；`destructive` 的再加一个红色 `破坏性` 徽标（+））｜操作 56px：
  **一个图标按钮**——收起时 `chevron-down`、提示 `查看入参`；展开时 `chevron-up`、提示 `收起`（`aria-expanded`）。展开后**在该行正下方、表格内**插一行（`colspan` 全宽，浅底 `--forebrain-bg-alt`），里面一个嵌套小表：参数｜类型｜必填（是/否）｜说明，数据从 `inputSchema.properties` + `required` 解析；没有参数时写 `无入参`。同时只展开一行。
- 分页器；说明：`本主代理运行时实际暴露给模型的工具。`

**7. 权限**（`PermissionsView.vue`）
- 工具栏：左 搜索框 `搜索规则内容`；右 `判定验证`、`添加规则`（主色）。
- 表格：动作（`允许` 绿 / `询问` 黄 / `拒绝` 红 徽标）｜工具（等宽）｜规则内容（等宽）｜操作 88px：`编辑`、`删除`（红）。
- 分页器；说明：`本主代理已记住的授权规则，写在 <路径>。默认审批模式在“设置 → 审批默认”；项目规则在各项目空间；单个会话可在输入框旁临时切换。`（路径等宽）

**8. 记忆**（`MemoriesView.vue` + 共用 `components/memory/MemoryFilesPanel.vue`）
- 工具栏：左 搜索框 `搜索记忆内容关键词`；右 （选中时）`已选 N 个`、`删除所选`（红，未选时禁用）、`重置全部`（红）。**删掉排序下拉框**。
- 表格 `forebrain-table--fixed`：勾选 40px｜记忆文件 300px（等宽，`核心` 灰徽标）｜内容（灰，一行截断）｜创建时间 150px（**表头排序**，首次 新→旧）｜更新时间 150px（**表头排序**，首次 新→旧）｜操作 88px：`编辑`（`pencil`）、`删除`（`trash-2` 红；核心文件为空位）。
  默认 更新时间 新→旧；换排序回第 1 页。时间 `YYYY-MM-DD HH:mm`、等宽、不折行。
- 分页器；说明：页面描述 + `核心文件（MEMORY.md、memory_summary.md）可以编辑、清空内容，但不能删除；每次保存或删除都会在记忆目录里留下一个版本。` + `记忆功能的开关在“设置 → 记忆”，项目记忆在各项目空间。`

## Current state（行号为 `4888d76`，已核实）

前端（全部用 `PageTopRow` 做标题行，见 `grep -rn '<PageTopRow' frontend/src`）：
- `views/ProjectsView.vue:4-12` 标题行（新建/刷新）；`:16-26` 搜索框 + **排序下拉**（`updated/name`）；`:30-50` 卡片式列表（名称、置顶/仅本项目记忆徽标、描述、路径·更新时间、三个文字按钮 打开/置顶/删除）；删除走 `ConfirmDialog` 的输入确认（保留）。
- `views/RulesView.vue:4-13` 标题行；`:20-45` 表格（文件/作用/更新时间/文字按钮“编辑”）。
- `views/SkillsView.vue:4` 标题行 + 安装按钮；`components/skills/SkillsTable.vue:3-31` 搜索 + 来源筛选（**固定五个来源**）+ 批量下载；`:38-136` 表格，操作列 `{{ t('skills.detail') }}` 文字按钮 + 下载/删除小图标（原生 `title`）。`SkillsTable` 也被 `components/project/ProjectSkills.vue` 与 `components/settings/SettingsSharedSkillsTab.vue` 使用。
- `views/SubagentsView.vue:4-15` 标题行；`:27-56` 表格（子代理/会话 id/状态/开始时间 + 文字按钮 打开会话/停止）。
- `views/ProvidersView.vue:4-13` 标题行；`:21-60` 表格 + 三个文字按钮（上移/编辑/删除）。
- `views/ToolsView.vue:4-12` 标题行；`:14-24` 搜索 + “显示 N / 共 M”；`:26-56` 卡片列表 + `查看入参`/`收起` 文字按钮，展开的是 JSON 原文。
- `views/PermissionsView.vue:4-13` 标题行；`:19-44` 表格 + 文字按钮 编辑/删除。
- `views/MemoriesView.vue:4` 标题行 + `MemoryFilesPanel`；`components/memory/MemoryFilesPanel.vue:3-33` 搜索 + **排序下拉** + 每页条数 + 重置；`:113-121` 只有上一页/下一页。`MemoryFilesPanel` 也被 `components/project/ProjectMemory.vue` 使用。

后端（Go，`pkg/gateway`）：
- `api_extra.go:5558-5585` `handleProjectsList`：`search`/`sort`/`limit`/`offset`，返回 `{"projects": [...]}`，无总数、无对话数。`state/project_store.go:606-646` `ListProjects` 已经 `pinned DESC` 在前，`sort=name` 时 `name COLLATE NOCASE ASC`，否则 `updated_at DESC`。
  `api_extra.go:5535-5556` `projectRow` 组装行（`trust_level`、`missing` 已有）。**聊天页的项目选择器**（`ChatView.vue` `forebrainApi.projectsList({ sort: 'updated' })`）与文件夹对话框也调这个接口——它们**不带 `page`**，必须保持原样。
- `api_extra.go:43-49` 规则路由；`handleAgentRuleFiles` 返回主代理规则文件列表（全量）。
- `api_extra.go:3937-3957` `handleSkillsListWith`（主代理与项目共用）返回 `{"installed": decorateSkillList(...), "actions": ...}`，全量。
- `agents_api.go:93` `handleAgentRoster` 返回整个名册。
- `api_extra.go:4991-5010` `handleProviders` GET 返回 `{"agent_id", "providers"}`，全量、有顺序。
- `api_extra.go:605-616` `handleToolsList` 返回**裸数组** `ToolMetas()`（字段 name/description/category/readOnly/destructive/inputSchema…）。
- `api_extra.go:671-700` `handlePermissionRules`：`source`/`behavior`/`session_id` 筛选，返回 `{"mode", "rules"}`，全量。
- `memories_api.go` `handleMemoriesFilesList`：**已是服务端分页**（071 已改用共用辅助）。
- 状态库：`fb_sessions(project_id, source, …)`，“对话”的口径见 `state/session_store.go:745` `conversationHoldsAMessage`（至少有一条消息）与 `ListSessionsForProject`（`source = SessionSourceConversation`）。

## Commands you will need

| 用途 | 命令 | 成功预期 |
|------|------|----------|
| state 包 | `CGO_ENABLED=1 go test -tags fts5 ./pkg/state -run 'ListProjects\|SessionCount' -count=1` | ok |
| gateway 包 | `CGO_ENABLED=1 go test -tags fts5 ./pkg/gateway -count=1` | ok |
| vet / fmt | `go vet ./pkg/... ./cmd/...` ；`gofmt -l cmd pkg` | 无输出 |
| 前端单测 / 类型 | `cd frontend && pnpm test` ；`npx vue-tsc --noEmit -p tsconfig.json` | 全绿 / exit 0 |
| 单个 e2e | `FOREBRAIN_E2E_SPEC=e2e/agent-tables.spec.ts scripts/acceptance/web_e2e.sh` | `web e2e: PASS` |
| 浏览器全套 | `scripts/acceptance/web_e2e.sh` | `web e2e: PASS` |

## Scope

**In scope**：
- Go：`pkg/state/project_store.go`（+测试）、`pkg/state/session_store.go`（只加计数查询，+测试）、`pkg/gateway/api_extra.go`（上述 6 个 handler）、`pkg/gateway/agents_api.go`（`handleAgentRoster`）、对应 `*_test.go`。
- 前端：`views/ProjectsView.vue`、`views/RulesView.vue`、`views/SkillsView.vue`、`components/skills/SkillsTable.vue`、`views/SubagentsView.vue`、`views/ProvidersView.vue`、`views/ToolsView.vue`、`views/PermissionsView.vue`、
  `views/MemoriesView.vue`、`components/memory/MemoryFilesPanel.vue`、`lib/api.ts`（这些接口的方法加分页参数/新字段类型）、`lib/time.ts`（新建 `formatDateTime`）、`lib/toolSchema.ts`（新建，解析 inputSchema）、`locales/index.ts`，各自的单测，
  `e2e/agent-tables.spec.ts`（新建）与因旧设计断言失败而必须更新的既有 e2e（只改断言）。
- `components/project/ProjectSkills.vue`、`components/settings/SettingsSharedSkillsTab.vue`、`components/project/ProjectMemory.vue`：**只**改到能配合新的 `SkillsTable`/`MemoryFilesPanel` 接口（传 scope/来源/筛选），它们自己的标题与布局留给 075/076。

**Out of scope**：MCP（074）；项目空间各标签（075）；设置各标签（076）；`PageTopRow.vue` 本身（076 删）；任何“不带 `page`”调用方的行为。

## Steps

### Step 1：后端——项目列表分页 + 对话数

1. `state/project_store.go`：新增 `ListProjectsPage(ctx, opts ListProjectsOptions, limit, offset int) (rows []Project, total int, err error)`，
   `ListProjectsOptions` 加 `Desc *bool`（nil = 该排序键的默认方向：`name` 升序、`updated` 降序）；ORDER BY 永远先 `pinned DESC`，再按键与方向，最后 `id`；
   `total` 用同一个 WHERE 的 `SELECT COUNT(*)`。原 `ListProjects` 保持签名与行为（内部可复用）。
2. `state/session_store.go`：新增 `ConversationCountsByProject(ctx, projectIDs []string) (map[string]int, error)`——一条 `GROUP BY project_id` 查询，
   条件与 `ListSessionsForProject` 相同（本主代理、`source = SessionSourceConversation`、`conversationHoldsAMessage`）。
3. `handleProjectsList`：`q := readListQuery(r)`；`q.Paged` 为假时**完全照旧**（仍读 `search/sort/limit/offset`，但每行加 `session_count`）；
   为真时 `Search = q.Q`（兼容：`q` 为空再看 `search`）、`Sort = q.Sort`（只认 `name`/`updated`）、方向 `q.Order`，用 `ListProjectsPage`，`writeListPage(w, "projects", rows, total, servedPage, q, nil)`。
   越界页：先算 total，`page` 超过最后一页时用最后一页的 offset。
4. 测试：`project_store_test.go` 加 `TestListProjectsPageKeepsPinnedFirstAndCountsTotal`（3 个项目、1 个置顶；name asc/desc、updated asc/desc 四种顺序置顶都在第一；total 与搜索一致）；
   `session_store_test.go` 加 `TestConversationCountsByProjectCountsOnlyConversationsWithMessages`（空会话、workshop 会话不计）；
   `api_extra_test.go` 加 `TestProjectsListPagesWithTotalAndSessionCount` 与 `TestProjectsListWithoutPageKeepsTheOldAnswer`（不带 page 时返回全部且无 `total` 也可，但每行有 `session_count`）。

**Verify**：`CGO_ENABLED=1 go test -tags fts5 ./pkg/state -run 'ListProjects|ConversationCounts' -count=1` ok；`./pkg/gateway -run 'ProjectsList' -count=1` ok。

### Step 2：后端——其余六个列表接口加服务端分页（内存列表用 `pageOf`）

每个都遵守 071 的约定：**不带 `page` 原样回答**；带 `page` 时“筛选 → 排序 → 切页”，保留原列表键 + `total/page/page_size`。
| 接口 | `q` 匹配 | 额外筛选 | 排序 | 新字段 |
|---|---|---|---|---|
| `GET /api/rules/agent` | 文件名、作用 | — | 原顺序 | — |
| `GET /api/skills`、`GET /api/v1/projects/:id/skills` | 名称、描述 | `origin`（`project/agent/shared/builtin`，可多值逗号分隔；共享技能库传 `shared,builtin`） | 原顺序 | — （`actions` 字段照旧放在顶层 extra） |
| `GET /api/agents/roster` | label、title（任务） | 与页面现在的过滤相同：`SubagentsView.vue:84` `runningRows = records.filter(row => String(row.status).toLowerCase() === 'running')`，搬到服务端，参数 `running=1` | 已运行时长 `elapsed_seconds` 长→短（即开始早的在前） | 每行 `session_title`（`s.Sessions.SessionTitle`，取不到为空串） |
| `GET /api/providers` | 服务商、模型、Base URL | — | 原优先级顺序（**不可改顺序**） | 每行 `index`（全局序号，前端“上移”用它） |
| `GET /api/tools` | 名称、描述 | `category`（精确）、`type`（`readonly` / `write`） | 名称 A→Z | 顶层 `categories`：**不受筛选影响**的全部分类去重列表（下拉选项用）。带 `page` 时响应改为对象 `{"tools": […], "categories": […], "total", "page", "page_size"}`；不带 `page` 时仍是**裸数组** |
| `GET /api/permissions/rules` | 工具名、规则内容 | 原 `source`/`behavior` | 原顺序 | `mode` 照旧在顶层 |

每个接口在对应的 `*_test.go` 加一条“带 page 的分页 + 搜索 + total”用例和一条“不带 page 原样”用例（照 Step 1 的写法）。

**Verify**：`CGO_ENABLED=1 go test -tags fts5 ./pkg/gateway -count=1` ok；`go vet ./pkg/...`、`gofmt -l pkg` 无输出。

### Step 3：前端——API 方法与共用小工具

- `lib/api.ts`：给 `projectsList`、`agentRuleFiles`、技能列表（主代理/项目）、`agentRoster`、`providers`、`toolsList`、权限规则 这些方法各加一个**分页版本**（命名 `xxxPage(query: ListQuery)`，用 071 的 `listParams`），
  返回 `{ rows, total, page, pageSize, …extra }`（把各自的列表键映射到 `rows`）。原方法不动（别处还在用）。`ProjectRecord` 加 `sessionCount?: number`；roster 行类型加 `sessionTitle?: string`；provider 行加 `index?: number`。
- `lib/time.ts`：`formatDateTime(unixSeconds?: number): string` → 本地时间 `YYYY-MM-DD HH:mm`，空值 `—`。单测。
- `lib/toolSchema.ts`：`toolParams(schema: unknown): Array<{ name: string; type: string; required: boolean; description: string }>`——读 JSON Schema 的 `properties`（保持声明顺序）、`required`、每个属性的 `type`（数组型写 `string[]` 这种：`items.type + '[]'`；没有 type 写 `object`）与 `description`。单测覆盖：无 properties、required 缺省、数组、嵌套 object。

### Step 4：逐页改造（每页一个子步骤，照“每页的目标结构”）

通用做法（每页都一样）：
1. 删 `PageTopRow`（及其 import）。页面最外层仍是现有的滚动容器；内容宽度沿用页面现有的 `max-w-*`。
2. 顶部 `ListToolbar`：`#left` 放搜索框（`class="forebrain-field h-8"`、`style="max-width:240px"`、`type="search"`，`v-model` 绑 `useServerList` 的 `q`）与筛选下拉；`#actions` 放原标题行的按钮（图标 + 文字，顺序照目标结构）。
3. 数据改用 `useServerList(api.xxxPage, { pageSize: 10, sort, order })`；加载/空态沿用现有文案（`common.loading`、各页 `empty`）。
4. 表格用 `<div class="forebrain-table-wrap"><table class="forebrain-table forebrain-table--fixed">` + `<colgroup>` 写死列宽；文字列内容包 `<span class="forebrain-cell-1" v-tip:top.trunc="全文">`。
5. 操作列 `<td class="text-right"><RowActions>…<RowAction :icon :label :name :tone @click />…</RowActions></td>`，缺的动作放 `<RowActionSlot />`。**不再有任何文字按钮或原生 `title`。**
6. 表格后 `<TablePager v-model:page v-model:page-size :total />`，再后 `<ListNote>`（说明文案逐字照目标结构；用 i18n 键，zh/en 都加）。
7. 原有弹窗（新建、编辑、删除确认、判定验证、安装）全部保留，只改触发它们的按钮。

子步骤 4.1 项目、4.2 规则指令、4.3 技能（改 `SkillsTable`：去掉内部的搜索/筛选/批量下载工具栏，改为由父页面通过 props 传 `scope`、`rows`、`loading`、选择集，工具栏由父页面用 `ListToolbar` 画；
`ProjectSkills.vue` / `SettingsSharedSkillsTab.vue` 先按同样方式接上工具栏与分页，**它们的标题行留给 075/076**）、4.4 子代理、4.5 模型服务、4.6 工具（展开行 + `toolParams`）、4.7 权限、
4.8 记忆（改 `MemoryFilesPanel`：去排序下拉、改表格 + `SortHeader` 两列 + `TablePager`；`ProjectMemory.vue` 只跟着传参）。
每完成一页跑一次 `pnpm test` 与 `vue-tsc`。

### Step 5：文案（zh/en）

新增或改用的键至少包括：各页搜索框占位（`projects.searchPlaceholder` 改为 `搜索名称或路径`、`rules.searchPlaceholder` `搜索规则文件`、`subagents.searchPlaceholder` `搜索子代理或任务`、`providers.searchPlaceholder` `搜索服务商或模型`、`tools.searchPlaceholder` `搜索工具名或说明`、`permissions.searchPlaceholder` `搜索规则内容`、`memories.searchPlaceholder` `搜索记忆内容关键词`）、
各页表下说明（逐字照目标结构）、工具分类中文名、`tools.type.readonly` `只读`、`tools.type.write` `有副作用`、`tools.destructive` `破坏性`、`tools.showSchema` `查看入参`、`tools.hideSchema` `收起`、`tools.noParams` `无入参`、
`tools.colParam/colType/colRequired/colDesc` `参数/类型/必填/说明`、`projects.colName/colRoot/colDesc/colSessions/colUpdated` `项目/路径/说明/对话/更新时间`、`projects.openSpace` `进入项目空间`、
`subagents.colTask/colSession/colElapsed` `任务/所在会话/已运行`、`subagents.openSession` `打开会话`、`skills.view` `查看`（替换 `skills.detail` 的用法）、`skills.downloadZip` `下载 zip`。
删除不再使用的键（`projects.sortUpdated/sortName`、`memories.sortUpdatedDesc…`、`tools.count`、`skills.detail`）。

### Step 6：e2e `frontend/e2e/agent-tables.spec.ts`（新建）+ 回归

用 API 造数据（照 `e2e/projects-modals.spec.ts`、`e2e/memory-files.spec.ts` 的造数方式），逐页断言：
1. 8 个页面内容区都**没有** `h2`；第一行是工具栏，最左是 `input[type=search]`。
2. 项目：造 23 个项目（1 个置顶）→ 分页器显示 `共 23 条`、3 个页码；点 `项目` 表头两次，第一行始终是置顶项目；`对话` 列显示造的对话数；点 `置顶` 图标后该行出现 `置顶` 徽标且排到第一。
3. 工具：分类下拉选项来自服务端；选 `只读` 后每行都是 `只读`；点第一行 `查看入参` 图标出现嵌套参数表，再点变 `收起`。
4. 记忆：点 `创建时间` 表头 → `aria-sort="descending"`、回到第 1 页；分页器页码可点。
5. 每页表格操作列里**没有**文字按钮（`button` 都带 `forebrain-row-action` 类），悬停任一图标出现 `role=tooltip`。
6. 技能（主代理）来源筛选只有 `全部来源/主代理/共享/内置`；操作列有 `查看` 眼睛图标、没有“详情”文字。
然后跑全套 `web_e2e.sh`；旧用例只允许因“标题行、文字按钮、排序下拉、卡片列表、`查看入参`文字按钮、`详情`文字”等旧设计改断言，逐条列在报告里；其它失败 → STOP。

## Test plan

Step 1/2 的 Go 用例（每个接口两条）；Step 3 的 `time`/`toolSchema` 单测；每页至少一条组件单测（渲染出工具栏 + 表格 + 分页器 + 无 `PageTopRow`）；Step 6 的 e2e。
截图 `$TMPDIR/073-<page>.png` 8 张，与原型同页并排对照，差异写进报告。

## Done criteria

- [ ] Go：`./pkg/state`、`./pkg/gateway` 全过；`go vet`、`gofmt` 无输出
- [ ] 前端：`pnpm test` 全绿、`vue-tsc` exit 0
- [ ] `grep -n 'PageTopRow' frontend/src/views/{Projects,Rules,Skills,Subagents,Providers,Tools,Permissions,Memories}View.vue` 无输出
- [ ] `grep -rn "t('skills.detail')\|projects.sortUpdated\|memories.sortUpdatedDesc" frontend/src` 无输出
- [ ] `agent-tables.spec.ts` 与全套 `web_e2e.sh` → `web e2e: PASS`
- [ ] `git status` 只出现 Scope 列出的文件

## STOP conditions

- Drift check 失败；071 未完成。
- 某接口加分页后，某个**不带 `page`** 的调用方的回答变了（测试会抓到）。
- `SkillsTable`/`MemoryFilesPanel` 的接口改动迫使 `ProjectSkills`/`SettingsSharedSkillsTab`/`ProjectMemory` 做超出“传参与接上工具栏/分页”的改动。
- 模型服务“上移”在分页后无法表达为全局顺序（例如 PUT 只接受整表）——报告接口形状再定。
- 子代理名册的“正在运行”过滤在服务端无法复现前端现有逻辑。

## Maintenance notes

- 新增菜单栏页面一律走这套：无标题、`ListToolbar`（左搜索必有）+ 表格 + `TablePager` + `ListNote`；接口按 071 约定服务端分页。
- 工具分类中文名表在 `locales`；引擎新增分类时只会原样显示英文键，补一条翻译即可。
- `SkillsTable`、`MemoryFilesPanel` 是三处共用的；改它们要同时看主代理页、项目空间、设置三处。
