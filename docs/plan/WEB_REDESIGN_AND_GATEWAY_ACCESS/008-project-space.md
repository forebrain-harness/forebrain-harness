# 计划 008：独立项目空间——项目列表、空间壳与概览/项目设置/项目会话/项目 MCP 四个标签

> **执行者须知**：逐步执行，每一步都运行"验证"命令并确认结果后再进入下一步。出现"STOP 条件"中的任何情况，立即停止并报告。
> 完成后更新 README 中本计划的状态行。**不要提交代码。**
>
> **漂移检查（先运行）**：
> `git diff --stat b92a1b1..HEAD -- frontend/src/views/ProjectsView.vue frontend/src/views/ProjectDetailView.vue pkg/state/project_store.go pkg/gateway/api_extra.go pkg/gateway/mcp_oauth_api.go`
> 计划 007 改过 `api_extra.go`（路由注册行）与 `router/index.ts`，那是预期的。

## 状态

- **优先级**：P1
- **工作量**：L
- **风险**：MED（后端 CRUD 已存在，主要是前端壳与新路由；靠既有 Go 测试 + 新前端测试 + 真机用例兜底）
- **依赖**：计划 007（最终菜单、`useTenantScope` 租户重置）；决策 D6 已定：**项目会话只列在项目空间**，对话抽屉不按项目分组
- **类别**：direction
- **基线**：提交 `b92a1b1`，2026-09-29

## 为什么要做

owner 要求："针对项目维度的所有配置和数据，必须设计单独的项目空间，支持用户在该项目空间内对配置进行读写操作。主代理维度和项目维度的配置和数据不能混在一起，必须边界清晰"；"项目空间里只展示项目维度的配置和数据，不展示继承的和共享的"（第 23 条，适用于所有标签页）。预览（`app-preview.html` captions 的 `project` 条，395 行）定义了项目空间的完整形态：顶部黑底"项目"标签与面包屑标明项目层，标签页为概览、规则指令、会话、记忆、权限、MCP、技能、定时任务；**技能标签例外**——它列出本项目能加载的全部技能（含继承），这是 owner 第 30 条明文取代第 22 条的唯一例外，由计划 010 实现；规则指令、记忆、权限、定时任务四个标签分别由计划 009、012、013 实现。本计划交付项目列表页重排、项目空间壳（面包屑 + 标签框架）、以及概览、项目设置、项目会话、项目 MCP 四个内容标签。

## 现状

- 后端能力已齐，本计划基本不写 Go：
  - `pkg/state/project_store.go`：`Project` 结构（`:27`，含 `ID/AgentID/Name/Icon/Description/Instructions/Root/ProjectKey/MemoryScope/ResourceAccess/Pinned/ArchivedAt`）、`Create :110`、`Get :160`、`GetByRoot :173`、`Update :199`、`SetPinned :241`、`Delete :273`（事务内解绑该项目的会话）。数据按 `agent_id` 租户隔离。
  - `pkg/gateway/api_extra.go`：项目路由注册在 `:109-129`（组为 `/api/v1/projects`）；`handleProjectsList :3335`（支持分页与 `q` 查询）、创建 `:3369`、详情 `:3481`、更新、置顶 `:3531`、归档 `:3553`、删除 `:3579`、项目会话创建 `handleProjectSessionsCreate :3605`、项目会话列表 `:3677`。
  - 项目 MCP：路由 `api_extra.go:119` `projects.Get("/:id/mcp", …)` 与 `:120` `projects.Post("/:id/mcp/consent", …)`；handler `pkg/gateway/mcp_oauth_api.go:356` `handleProjectMCPPreview`（GET，预览将加载的 MCP 与工具数）、`:418` `handleProjectMCPConsent`（POST，信任确认）。
  - 项目技能信任门与启动挂接：`pkg/skill/roots.go:113` `TrustedProjectSkillRoots`（`safety.IsTrusted` 决定项目技能是否加载）；`api_extra.go:2596` 起的项目技能路由由计划 010 使用。
- 前端：`frontend/src/views/ProjectsView.vue`（247 行）与 `ProjectDetailView.vue`（240 行）是早期简易版，无标签页结构、无面包屑层级感，`ProjectDetailView` 混杂项目设置与少量数据。路由 `/projects`、`/projects/:id` 已注册（`router/index.ts`）。
- 预览的项目空间交互（captions `project` 条 + 页面片段）：点顶部面包屑"项目"回到列表；"仅本项目"的记忆召回开关、"允许读取项目目录之外的资料"开关属于项目设置；项目 MCP 卡片带"必需（连接失败时本项目会话不启动）"标记与信任按钮。
- README 全局规则（90-115 行）全部适用。

## 设计

### 1. 项目列表页（`/projects`）

按预览 `projects` 条：页头"项目 · 主代理 + <当前主代理名>"作用域徽标；卡片网格列出**当前主代理**的项目（名称、图标、描述、根目录、更新时间、置顶标记），卡片操作"进入项目空间"；页头操作：新建项目、筛选（全部/置顶/已归档）。新建项目对话框字段：项目名、图标（emoji）、描述、项目目录（必须已存在的绝对目录，预览文案："项目的规则指令、记忆、权限、MCP 与技能都以这个目录为键；目录必须已存在。创建后进入项目空间继续设置"）。列表操作保留：置顶/取消置顶、归档/恢复、删除（二次确认，明示"会话会与项目解绑，数据保留"）。

### 2. 项目空间壳（`/projects/:id/*`）

`ProjectDetailView.vue` 重构为壳组件：
- 顶部黑色（`--rail-bg` 系深色，不随菜单栏深浅变化）横条：左端"项目"徽标 + 项目名面包屑（点"项目"回列表），右端项目根目录路径展示。
- 下方标签栏（`projTab` 状态同步到路由）：概览 `overview`、规则指令 `rules`、会话 `sessions`、记忆 `memory`、权限 `perm`、MCP `mcp`、技能 `skills`、定时任务 `cron`。本计划实现 overview 与设置（并入 overview 的"项目设置"卡）、sessions、mcp；rules 由 009、memory 由 012、perm 由 009、skills 由 010、cron 由 013 各自填充——壳先渲染占位空态（"该标签由后续计划交付"不可接受为最终态，占位组件只在本计划验收时存在，由后续计划替换）。
- 路由改为嵌套：`/projects/:id` 重定向到 `/projects/:id/overview`；新增 `/projects/:id/(overview|rules|sessions|memory|perm|mcp|skills|cron)` 八条子路由。URL 直接可分享。
- **边界铁律（owner 第 23 条）**：除技能标签外，项目空间任何标签不得展示全局或主代理层的数据；每个标签页头放"项目"作用域徽标。项目切换（进入另一个项目空间）时同样走 `useTenantScope` 的重置机制（复用 007 的 composable，项目维度加一个 `activeProjectId` 键）。

### 3. 概览与项目设置（overview 标签）

- 概览卡：项目描述、根目录、`ProjectKey`、创建/更新时间、归档状态。
- 项目设置卡（可编辑，走 `Update :199`）：名称、图标、描述、**项目指令**（`Instructions`，多行文本，成为本项目会话的注入指令）、**记忆作用域选择**（预览"记忆作用域"卡：`MemoryScope`——仅本项目 / 允许读取全局记忆，文案"只召回本项目自己的记忆"）、**允许读取项目目录之外的资料**（`ResourceAccess` 开关）、置顶。
- 危险区：归档/恢复、删除项目（与列表页同一确认语义）。

### 4. 项目会话（sessions 标签）

- 列表：`GET /api/v1/projects/:id/sessions`（`api_extra.go:3677`）分页列出**只属于本项目**的会话（标题、更新时间、消息数）；行点击跳转对话页并打开该会话（`/?session=<id>`，与 007 子代理页同一跳转约定）。
- 头部"新的项目会话"按钮 → `POST /api/v1/projects/:id/sessions`（`handleProjectSessionsCreate :3605`）→ 跳转新会话。
- 对话抽屉（005）保持只列非项目会话，抽屉底部说明"项目里的会话在各自的项目空间中"——本计划不改抽屉，只验收这条边界仍然成立。

### 5. 项目 MCP（mcp 标签）

- `GET /api/v1/projects/:id/mcp`（**注意：没有 `/preview` 后缀**；注册在 `pkg/gateway/api_extra.go:119`，handler 为 `mcp_oauth_api.go:356` `handleProjectMCPPreview`）渲染"项目 MCP 服务"卡片列表：服务名、URL、传输类型、工具数、必需标记；"添加项目 MCP 服务"引导用户编辑项目目录下的 `.forebrain/mcp_servers.yaml`（读取/写入走该文件，本计划如发现缺读写 API 则 STOP 上报，见 STOP 条件），预览刷新按钮重新拉该端点。
- 未信任的项目显示"信任此项目"按钮 → `POST /api/v1/projects/:id/mcp/consent`（注册在 `pkg/gateway/api_extra.go:120`，handler `mcp_oauth_api.go:418`）；预览文案："信任后才会加载项目里的技能、MCP 与权限规则。"

## 需要的命令

| 用途 | 命令 | 成功标志 |
| --- | --- | --- |
| Go 测试（回归，确认不改后端） | `CGO_ENABLED=1 go test -tags fts5 ./pkg/gateway/... ./pkg/state/... -count=1` | 全部 `ok` |
| 前端单测 | `cd frontend && corepack pnpm test` | 全部通过 |
| 前端构建 | `cd frontend && corepack pnpm build --outDir "$TMPDIR/fb-webui" --emptyOutDir` | 退出码 0 |
| 真机验证 | `scripts/acceptance/web_e2e.sh` | 最后一行 `web e2e: PASS` |
| 缓存不变式 | `git diff --stat -- pkg/agent pkg/assembly pkg/llm pkg/turn/prompt* internal/agentrun` | 无输出 |

## 范围

**只允许修改**：
- `frontend/src/views/ProjectsView.vue`（重写）、`frontend/src/views/ProjectDetailView.vue`（重写为壳）
- `frontend/src/components/project/*`（新建：标签栏、概览、项目设置、会话列表、MCP 卡片等）
- `frontend/src/router/index.ts`（嵌套子路由）、`frontend/src/App.vue`（仅当菜单徽标需要项目计数）、`frontend/src/locales/index.ts`
- `frontend/src/composables/useTenantScope.ts`（扩展 `activeProjectId`）、`frontend/src/lib/api.ts`（补缺失的类型与调用封装，不新增后端端点）
- `frontend/e2e/project-space.spec.ts`（新建）

**不要碰**：
- `pkg/**`（后端 CRUD/会话/MCP 端点已齐；发现缺口走 STOP 条件）
- 项目空间内 rules/memory/perm/skills/cron 五个标签的实现（009/010/012/013 的范围）；占位组件允许存在，但不得实现任何这五个标签的业务逻辑。
- 对话抽屉（005 交付物）。

## 步骤

### 第 1 步：路由与壳

实现设计第 2 条（嵌套路由、黑底横条、面包屑、标签栏、`activeProjectId` 重置）。五个未交付标签渲染占位空态组件。

**验证**：`cd frontend && corepack pnpm test` 全部通过；`grep -c "projects/:id" frontend/src/router/index.ts` ≥ 2。

### 第 2 步：项目列表页

实现设计第 1 条（含新建对话框、置顶/归档/删除）。

**验证**：`cd frontend && corepack pnpm test` 全部通过。

### 第 3 步：概览/设置、会话、MCP 标签

实现设计第 3、4、5 条。

**验证**：`cd frontend && corepack pnpm test` 全部通过；`CGO_ENABLED=1 go test -tags fts5 ./pkg/gateway/... -count=1` 全部 `ok`（确认后端零改动）。

### 第 4 步：真机验证

新建 `frontend/e2e/project-space.spec.ts`：
1. `/projects` 新建项目（目录用 e2e 脚手架的临时工作区子目录），卡片出现；截图 `projects-list.png`。
2. 进入项目空间：黑底横条含项目名；默认落在 overview；URL 为 `/projects/<id>/overview`。
3. 项目设置：改描述、切"仅本项目"记忆作用域、开"允许读取项目目录之外的资料"，刷新后仍保持。
4. sessions 标签：点"新的项目会话"创建成功并跳转对话页；回到项目空间会话列表出现该会话；对话抽屉里**不出现**该项目会话（边界断言）。
5. mcp 标签：项目未信任时显示"信任此项目"；点击后 preview 列出 0 个服务（空态）或既有服务；截图 `project-mcp.png`。
6. 面包屑"项目"返回列表；切换到另一个主代理后项目列表变化（租户隔离断言）。

**验证**：`scripts/acceptance/web_e2e.sh` → 最后一行 `web e2e: PASS`。

## 完成标准（全部满足）

- [x] 第 1-3 步验证全部通过（router 嵌套 8 子路由、壳+占位、列表重排、overview/settings/sessions/mcp 四标签）
- [x] `web e2e: PASS`（37/37，project-space 6 用例）
- [x] 本计划零后端改动（`pkg/` 中的既有改动全部来自计划 001–007，008 会话内未触碰任何 pkg 文件；gateway/state 回归全绿作为佐证）
- [x] e2e 断言通过（抽屉不含项目会话、项目空间只渲染本项目数据）
- [x] 均无输出
- [x] README 状态行已更新

## STOP 条件

- "现状"摘录与代码对不上（尤其项目路由注册行、`Project` 字段）。
- 发现项目 MCP 的 `.forebrain/mcp_servers.yaml` 读写没有现成 API、需要新增 Go 端点——超出"本计划不写 Go"的前提，先报告 owner 拍板（可能扩本计划或另立小计划）。
- 发现 `handleProjectSessionsCreate` 的会话没有落到 `fb_sessions.project_id`（`pkg/state/schema.sql:36`）而是别的机制。
- 需要修改"范围"之外的文件。

## 维护说明

- 项目空间的标签数组在壳组件里集中定义；009/010/012/013 接入时替换对应占位组件并删掉占位，不改壳。
- "除技能标签外只展示项目自己的数据"是 owner 明文规则；任何后续标签实现先对照这条。
- 项目删除是配置层操作：会话解绑（`Delete :273` 事务）、项目目录与记忆数据保留。
