# Plan 075：项目空间——面包屑进顶栏、“项目设置”标签（打平的表单，含项目指令与记忆作用域、删除项目）、各标签无标题 + 搜索 + 表格 + 分页 + 表下说明；会话/定时任务/通道改表格，后端补字段与分页

> **执行者须知**：按步骤顺序执行，每步跑验证命令并确认预期结果后再进下一步。触发
> "STOP conditions" 时停下报告，不要即兴发挥。**不要更新 `plans/README.md`**（评审方维护索引）。
> **禁止提交代码**（owner 手动提交）：不建分支、不建 worktree、不 commit、不 push、不 stash；改动留在当前工作区。
> 先跑 `scripts/fetch-libtokenizers.sh`；`go test` 报 pdfium 找不到时先 `make pdfium pdfium-fonts` 并照 Makefile 导出 `PKG_CONFIG_PATH`。
>
> **行号说明**：本计划的行号按 `4888d76`；071–073 落地后 `lib/api.ts`、`locales/index.ts`、`App.vue`、`main.css`、`api_extra.go` 等会偏移，**一律用给出的代码片段 grep 定位**，行号只作参考。
> **前置**：plan 071、073 已完成（`SkillsTable`、`MemoryFilesPanel` 已是新接口；`lib/time.ts` 的 `formatDateTime` 存在）；plan 074 已完成（项目 MCP 标签已换成 `McpServersTable`）。
> **Drift check（先跑）**：以下每条都必须各有输出，否则 STOP：
> ```
> test -f frontend/src/lib/time.ts && grep -n 'McpServersTable' frontend/src/components/project/ProjectMcp.vue
> grep -n '<div class="proj-head">' frontend/src/views/ProjectDetailView.vue
> grep -n "{ key: 'overview', routeName: 'project-overview', label: t('projects.tabOverview') }" frontend/src/views/ProjectDetailView.vue
> grep -n "if (!window.confirm(t('projects.deleteConfirm'))) return" frontend/src/components/project/ProjectOverview.vue
> grep -n 'data-testid="project-instructions"' frontend/src/components/project/ProjectRules.vue
> grep -n 'rows = append(rows, map\[string\]any{"id": item.ID, "title": sessionDisplayTitle(item), "updated_at": item.UpdatedAt})' pkg/gateway/api_extra.go
> ```

## Status

- **Priority**: P1
- **Effort**: L
- **Risk**: MED（10 个标签 + 4 个接口；会话列表加几个计算字段，要注意查询代价）
- **Depends on**: plans/071-web-list-foundation.md、plans/073-web-agent-pages-as-tables.md、plans/074-web-mcp-three-pages-and-drawer.md
- **Category**: direction（设计稿落地）
- **Planned at**: commit `4888d76` + 工作区，2026-10-18

## Why this matters

owner 2026-10-18 在设计稿上重排了项目空间：面包屑（项目 › 名称、目录、信任状态）放进**顶栏**（与技能工坊一致），顶栏下面直接是标签；
“概览”改名**项目设置**，只剩一张**打平的表单**（无外框、无表单标题），**项目指令**（原在规则指令标签）与**记忆作用域**（原在记忆标签，改成标准单选组）都并进来，
“删除项目”在“保存”右侧，统计卡片与“危险操作”卡片去掉；其余每个标签都**没有标题**、表格上方左侧**搜索框**、右侧按钮、表格下方分页器与说明；
会话、定时任务、通道改成**表格**。owner 裁决：所有列表**服务端分页**；原型没画的现有功能（LSP 标签、定时任务的编辑/最近状态/执行记录、18 种通道、项目图标）**保留**并按同一套规则放；
会话表的模型/消息数/创建时间/心跳与通道的“是否已配置”由**后端补字段**。

## 设计稿（验收依据，实施须 1:1 还原）

- 原型 https://claude.ai/artifact/KKfPW8dXWScuRdTzonsk2F（本地 `docs/plan/WEB_REDESIGN_AND_GATEWAY_ACCESS/app-preview.html`）。
  状态按钮：**项目 · 项目设置**、**项目 · 会话**、**项目 · 定时任务**、**项目 · 新建定时任务**、**项目 · 通道**、**项目 · 编辑通道（弹窗）**、**项目 · 技能**、**项目 · MCP 三层**，以及在项目空间里直接点各标签。
- 原型代码：`topbar()` 里 `S.page === 'project'` 的分支（面包屑）、`pages.project`（标签与各标签内容）、`cronBlock`、`channelsBlock`、`fmModal` 的 `chanedit`、`radioField`、`tabTools`、`tabNote`；
  CSS `.proj-head`（`padding: 4px 28px 0`，只剩标签行）、`.top .crumb`、`.top .crumb > .t-name`、`.top .meta`。

### 目标结构（逐项，“+”= 原型没画、按 owner 裁决保留的现有功能）

**顶栏**（`ProjectDetailView` 用 `useTopbar().set({ leading })` 贡献）：面包屑 `项目`（链接回项目列表）`›` 项目名（`.t-name`：最大 360px、截断、`v-tip:bottom.trunc`）+ 目录（等宽灰、`.meta` 可缩短截断）+ 信任徽标（`已信任` 绿 / `未信任` 灰）。页面里原来的 `.crumb` 行删除。
**标签行**（顶栏正下方）：`项目设置`、`规则指令`、`会话`、`记忆`、`权限`、`MCP`、`LSP`（+，保持现有位置）、`技能`、`定时任务`、`通道`。

**项目设置**（`ProjectOverview.vue`，路由名保持 `project-overview`，文案改“项目设置”）——表单直接铺在标签页里，**无外框、无标题**，从上到下：
1. 两列：`项目名`（输入）｜`绑定的工作目录`（只读、等宽）
2. `说明`（单行输入，整行）
3. 两列（+）：`图标`（保留，单行输入，可放 emoji）｜`项目键`（只读、等宽，`projectKey`）
4. `项目指令`（多行，3 行）+ 下方帮助 `注入本项目每个会话的开头，新会话生效。`
5. `记忆作用域`：`RadioField`（横排）选项 `共享（可跨项目召回）`=`shared`、`仅本项目`=`project_only`；帮助文字随选中变化：`本项目的会话也能召回其他记忆；修改只在新会话生效。` / `只召回本项目自己的记忆；修改只在新会话生效。`
6. 开关行（每行：左边标题 13px/500 `--forebrain-text` + 下一行灰字说明 12px `--forebrain-muted-text`，右边 `components/common/SwitchComponent.vue`；行间 1px 分隔线 `--forebrain-divider`；无外框）：`信任此目录`（`信任后才会加载项目里的技能、MCP 与权限规则。`）、`允许读取项目目录之外的资料`（`关闭后，本项目会话不能读取文件库与跨项目记忆。`）
7. 按钮行：`保存`（主色，未改动时禁用）｜`删除项目`（红色描边）——删除走与项目列表**同一个**“预览影响 + 输入项目名确认”弹窗（`ConfirmDialog` 的 `typedConfirm`），删除成功后回到 `/projects`。
（“置顶”不再出现在这里——项目列表的图钉图标就是入口；原概览卡片里的创建/更新时间不再显示。）

**规则指令**：工具栏左 `搜索规则文件`、右 `新建规则文件`；表格（文件｜作用｜更新时间｜操作 `编辑`）；分页；说明 `项目目录里的规则文件，保存后新会话生效。`（未信任时仍显示现有的信任门，表格不出现）。项目指令**不再**在这里。
**会话**：工具栏左 `搜索会话标题`、右 `新建项目会话`；表格 `forebrain-table--fixed`：标题（自适应；链接，点开会话；截断）｜模型 150px（等宽）｜消息 64px（右对齐等宽）｜心跳 100px（`每 N 分钟` 品牌徽标 / `已暂停` 灰徽标 / `—`）｜
创建时间 150px（**表头排序**，首次新→旧）｜更新时间 150px（**表头排序**，首次新→旧；默认）｜操作 56px：`打开`（`message-square`）。分页；说明 `项目会话都在项目根目录运行。`
**记忆**：`MemoryFilesPanel`（073 已改成表格）；说明 `可以查看、编辑、删除过时或错误的记忆。` + 核心文件说明。记忆作用域**不再**在这里。
**权限**：（未信任时信任门）工具栏左 `搜索规则内容`、右 `规则匹配测试`、`添加规则`（主色）；表格同主代理权限页；分页；说明 `项目已信任时生效；规则写在 .forebrain/safety.json。`（路径等宽）
**MCP**：074 已完成，不动。
**LSP（+）**：（未信任时信任门）工具栏左 `搜索语言服务`、右 `刷新`；表格：语言服务（等宽 id）｜说明（`summary`；已允许/已拒绝的条目后端只有 id，显示 `—`）｜状态（`待确认` 黄 / `已允许` 绿 / `已拒绝` 灰）｜操作：待确认 → `允许`（`check`）`拒绝`（`x` 红）；其余空；分页；说明 = 现有 `projects.lspDescription`。
**技能**：073 执行时**已经**给 `ProjectSkills.vue` 接好了 `ListToolbar` + `TablePager` + `ListNote`（`ProjectSkills.vue` 里搜 `<ListToolbar>`）——本计划只删它顶部的 `PageTopRow`，并把 `ListNote` 文案改成： `本项目实际能加载的全部技能。同名时按 项目 → 主代理 → 共享 → 内置 覆盖；只有项目技能可在这里修改。`
**定时任务**：工具栏左 `搜索任务`、右 `新建`（主色）；表格 `forebrain-table--fixed`：名称（自适应，加粗截断）｜计划 140px（等宽）｜投递至 200px（等宽；`只记录` 灰）｜下次执行 110px（`MM-DD HH:mm`，用 Step 6 新增的 `formatMonthDayTime`；暂停时 `已暂停` 黄徽标）｜
最近一次 96px（+：`成功` 绿 / `失败` 红 / `运行中` 品牌；失败时悬停提示失败原因（沿用现有 `jobErrorText`））｜已执行 64px（`N 次`）｜启用 56px（开关 = 暂停/恢复）｜
操作 140px：`立即执行`（`play`）、`编辑`（+ `pencil`，打开现有编辑器）、`执行记录`（`history`）、`删除`（`trash-2` 红）。分页；说明 `定时任务属于本项目，在项目根目录执行。`
执行记录：改为**弹窗**（`ForebrainModal` md，标题 `执行记录 · {任务名}`），里面一张表（状态｜开始时间｜触发｜投递至｜操作：`打开对话`（`message-square`，有对话时）、`查看输出`（`chevron-down`/`chevron-up`，行下展开输出与错误原文））+ 分页（服务端）。
**通道**：工具栏左 `搜索通道`、右 `刷新`（**去掉页面级“保存”**）；表格 `forebrain-table--fixed`：通道 130px（显示名，见 Step 2 的对照表）｜投递标识 230px（等宽；= 定时任务“投递至”里用的值，即通道 kind；未配置为 `—`）｜
配置（自适应：已配置时列出非空字段 `{键名} {值}`（键名就是配置里的 snake_case 键，如 `app_id`），密钥字段显示后端给的 `secret_hints[键]`（`••••` + 末 4 位），用 ` · ` 分隔；未配置写 `未配置` 灰）｜启用 56px（开关；**未配置时禁用**并提示 `先编辑，填好配置`）｜操作 56px：`编辑`（`pencil`）。
编辑弹窗（`ForebrainModal` md，标题 `编辑通道 · {显示名}`）：该通道的全部字段两列排布，标签 = 等宽的键名（18 种通道没有字段可读名的来源表，**直接显示键名**，不另造中文名）；密钥字段空时 `type="password"`；有已存密钥时底部提示 `密钥已脱敏，不修改即保留原值。`；按钮 `取消` / `保存`。
开关与弹窗保存都立即写入（不再有页面级保存）。分页；说明 `本项目的消息通道；定时任务可投递到已启用的通道。`

## Current state（行号为 `4888d76`，已核实）

- `views/ProjectDetailView.vue:5-24` `.proj-head` 里有面包屑行（`项目 › 名称 目录 已信任`）+ 标签行；`:51-62` 标签数组（`overview` 文案 `projects.tabOverview` = 概览；含 `lsp`）。
- `components/project/ProjectOverview.vue:1-96` 三张 `CardComponent`：概览信息（描述/根目录/ProjectKey/更新/创建）、项目设置（名称、图标、描述、记忆作用域两个**大按钮**、`resourceAccess` 复选框、`pinned` 复选框、保存）、危险操作（删除，`window.confirm`）；
  `:130-160` 保存：`projectUpdate({ name, icon, description, memoryScope, resourceAccess })` + 置顶变化时 `projectPin`。`lib/api.ts:2392` `projectUpdate` 还接受 `instructions`、`trust`。
- `components/project/ProjectRules.vue:3-24` 信任门 + **项目指令**（标题行 + 保存按钮 + textarea `data-testid="project-instructions"`）；`:26-` 规则文件表（标题行）。
- `components/project/ProjectSessions.vue:3` 标题行；列表只有标题与更新时间（`lib/api.ts:2415` `projectSessions(id)` 不带参数）。
- `components/project/ProjectMemory.vue:3` 标题行 + `MemoryFilesPanel`。
- `components/project/ProjectPerm.vue:17` 标题行 + 信任门 + 表格。
- `components/project/ProjectLsp.vue:4` 标题行 + 信任门 + 待确认列表（允许/拒绝文字按钮）+ 已允许/已拒绝两个列表。
- `components/project/ProjectSkills.vue:3` 标题行 + `SkillsTable`。
- `components/project/ProjectCron.vue` 包 `views/CronView.vue`：`:3` 标题行（刷新、新建）；`:15-60` 卡片列表（状态徽标、下次执行/暂停、投递、已执行、失败原因；五个文字按钮 立即执行/暂停|恢复/编辑/执行记录/删除）；执行记录在卡片里展开；编辑器是自写的遮罩（保留不改）。
- `components/project/ProjectChannels.vue:3-48` 标题行（刷新、**保存**）+ 每种通道一张卡片（开关 + 启用后展开全部字段输入框）；`:111-125` 保存把整个 `channels` map PUT 回去。
- 后端：
  - `api_extra.go:5848-5870` `handleProjectSessionsList`：`limit/offset`，行只有 `id/title/updated_at`；`state/session_store.go:831-869` `ListSessionsForProject`（`source = SessionSourceConversation` 且至少有一条消息，`ORDER BY updated_at DESC`）。
    `fb_sessions.created_at` 存在；会话模型在 `fb_session_model_state(session_id, provider, model, effort)`（`session_store.go:2590-2600`）；心跳 `svc.ListHeartbeats(ctx, agentID)`（`api_extra.go:5454-5476`，`heartbeatResponse` 给 `interval_seconds`、`paused`）。
    `fb_messages(role ∈ user/assistant/tool/reasoning/system, visibility ∈ visible/repaired/withdrawn/rewound)`。
  - `api_extra.go:6080` `handleProjectRuleFiles`、`:6348` `handleProjectPermissionRules`、`:6643` `handleProjectLSPPreview`：全量。
  - `api_extra.go:5235` `handleCronJobs`：返回 `{"agent_id","project_id","records"}` 全量；`:5358-5364` 执行记录只有 `limit`（默认 50）。
  - `api_extra.go:4665-` `handleChannels`：GET 返回 `{"channels": map[kind]fields, "kinds": [...]}`（`channel.ChannelKinds` 全部 kind，没存的 kind 用 `config.ChannelKindDefaults(kind)` 填默认值）；**PUT 收整个 map，并且会清掉 map 里没有的 kind**（`api_extra.go` 里 `saveProjectChannels` 的注释 “writes the kinds a page submitted and clears the ones it dropped”）。没有“是否已配置”。
    密钥打码：`redactChannelFields`（搜 `func redactChannelFields`）用 `config.IsSecretLikeConfigKey(k)` 判断密钥键，把值换成 `config.RedactedSecretPlaceholder`（`[REDACTED]`，**没有末 4 位**）；模型服务的“末 4 位”提示来自 `providerKeyHint(home, key)`（搜 `func providerKeyHint`）。
  - 执行记录：`pkg/state/cron.go` `func (s *CronStore) ListRuns(ctx, jobID string, limit int)`（`limit<=0||>200` 时置 50，没有 offset、没有计数）；`pkg/process/cron_service.go` `func (c *CronService) JobRuns(ctx, agentID, jobID string, limit int)`。
  - `fb_messages` 有索引 `idx_fb_messages_session_visibility(session_id, visibility)`（`schema.sql`），按会话数消息可以走索引。
- 前端：`lib/api.ts` 的响应拦截器把响应**深层**转 camelCase（搜 `response.data = toCamelCase(response.data)`）——通道字段的键（`app_secret` 等）、顶层以 kind 为键的 map 都会被改名，见 Step 2/6 的处理。
- 073 落地后的共用件接口（写 075 时以工作区实际代码为准）：`SkillsTable` props `rows/loading/saving/scope/selected`、emits `toggle/delete/download/detail/update:selected`；`useServerList` 的 fetch 可以在闭包里顺手取响应的顶层额外字段（`ToolsView.vue` 取 `categories` 就是这样做的，照它）。

## Commands you will need

| 用途 | 命令 | 成功预期 |
|------|------|----------|
| state 包 | 见表格下方的代码块 | ok |
| gateway 包 | `CGO_ENABLED=1 go test -tags fts5 ./pkg/gateway -count=1` | ok |
| vet / fmt | `go vet ./pkg/... ./cmd/...` ；`gofmt -l cmd pkg` | 无输出 |
| 前端 | `cd frontend && pnpm test` ；`npx vue-tsc --noEmit -p tsconfig.json` | 全绿 / exit 0 |
| 单个 e2e | `FOREBRAIN_E2E_SPEC=e2e/project-space-tables.spec.ts scripts/acceptance/web_e2e.sh` | `web e2e: PASS` |
| 全套 e2e | `scripts/acceptance/web_e2e.sh` | `web e2e: PASS` |

state 包命令（`-run` 里用 `|`，**不要**写成反斜杠加竖线——Go 正则会把它当字面量，一个都不匹配却显示 ok）：
```
CGO_ENABLED=1 go test -tags fts5 ./pkg/state -run 'ListSessionsForProject|ListRunsPage' -count=1
```

## Scope

**In scope**：
- Go：`pkg/state/session_store.go`（项目会话分页查询）、`pkg/state/cron.go`（新增 `ListRunsPage`）、`pkg/process/cron_service.go`（新增 `JobRunsPage`）、`pkg/gateway/api_extra.go`（`handleProjectSessionsList`、`handleProjectRuleFiles`、`handleProjectPermissionRules`、`handleProjectLSPPreview`、`handleCronJobs`、执行记录 handler、`handleChannels`）、`pkg/gateway/channel_labels.go`（新建：通道显示名表，供搜索）及以上各包的测试。
- 前端：`views/ProjectDetailView.vue`、`components/project/*.vue`（Overview、Rules、Sessions、Memory、Perm、Lsp、Skills、Cron、Channels）、`views/CronView.vue`、`components/cron/CronRunsDialog.vue`（新建）、
  `components/project/ChannelEditDialog.vue`（新建）、`lib/api.ts`（这些接口的分页版本与新字段）、`lib/channelLabels.ts` 与 `lib/channelLabels.json`（新建，见 Step 2）、`lib/time.ts`（新增 `formatMonthDayTime`）、`components/project/ProjectDeleteDialog.vue`（新建）、`views/ProjectsView.vue`（**只**改成使用 `ProjectDeleteDialog`）、`components/workshop/WorkshopTopbar.vue`（**只**改面包屑外层类名，见 Step 3）、`assets/main.css`（顶栏面包屑类）、`locales/index.ts`、相关单测、`e2e/project-space-tables.spec.ts`（新建）及因旧设计断言失败必须更新的既有 e2e（只改断言，主要是 `project-space.spec.ts`、`cron-*.spec.ts`、`rules-permissions.spec.ts`）。

**Out of scope**：项目 MCP 标签（074 已做）；定时任务编辑器与 `ScheduleBuilder` 本身（只改触发它的按钮）；通道的写入语义（PUT 仍收整个 map）；`PageTopRow.vue` 的删除（076）。

## Steps

### Step 1：后端——项目会话列表：分页、排序、搜索 + 模型/消息数/创建时间/心跳

1. `state/session_store.go` 新增 `ListSessionsForProjectPage(ctx, projectID string, opts ProjectSessionsQuery, limit, offset int) ([]ProjectSessionRow, int, error)`：
   - 条件与 `ListSessionsForProject` 完全相同（本主代理、`project_id`、`source = SessionSourceConversation`、`conversationHoldsAMessage`），外加 `opts.Search`（`title LIKE`）。
   - 排序键 `created` / `updated`（默认 `updated`），方向默认降序，次序 `id`。
   - 每行带：`ID, Title, CreatedAt, UpdatedAt, Model`（`LEFT JOIN fb_session_model_state` 取 `model`，没有为空串）、`MessageCount`（**口径**：`visibility = 'visible'` 且 `role IN ('user','assistant')` 的消息数，用相关子查询 `(SELECT COUNT(*) FROM fb_messages m WHERE m.session_id = fb_sessions.id AND m.visibility = 'visible' AND m.role IN ('user','assistant'))`，走 `idx_fb_messages_session_visibility`）。
     若 `/api/chat/sessions/:id/messages` 的显示逻辑另外排除了“环境上下文”消息，先找到它判断环境上下文的条件并在这里同样排除；找不到明确条件就按上面口径，报告里写明。
   - `total` 用同一 WHERE 的 `COUNT(*)`。原 `ListSessionsForProject` 不动。
2. `handleProjectSessionsList`：`q := readListQuery(r)`；不带 `page` 时**原样**（`limit/offset`，只有 `id/title/updated_at`）；带 `page` 时用新查询，行加 `created_at`、`model`（**为空就留空**，前端显示 `—`）、`message_count`、
   `heartbeat`（**不要用 `s.cronScope(w)`**——它失败时会往响应里写错误；直接 `svc := s.cron()`、`active, err := s.activePrimarySummary()`，任一取不到就整列不输出心跳、列表照常返回；取到时 `svc.ListHeartbeats(ctx, active.ID)` 一次取全，按 `session_id` 建 map；有则 `{"interval_seconds", "paused"}`，无则不输出该键），`writeListPage(w, "sessions", …)`。
3. 测试：`session_store_test.go` `TestListSessionsForProjectPageSortsCountsAndSearches`（两条会话不同创建/更新时间、一条带模型选择、消息里有 tool/reasoning/withdrawn 不计）；`api_extra_test.go` `TestProjectSessionsListPagedCarriesModelCountAndHeartbeat` 与 `TestProjectSessionsListWithoutPageKeepsTheOldAnswer`。
4. 查询计划：在新测试里对新查询跑 `EXPLAIN QUERY PLAN`，断言输出里**没有** `SCAN m`/`SCAN fb_messages`（数消息必须是 `SEARCH … USING INDEX idx_fb_messages_session_visibility`）。

**Verify**：`CGO_ENABLED=1 go test -tags fts5 ./pkg/state -run 'ListSessionsForProject' -count=1` ok；`CGO_ENABLED=1 go test -tags fts5 ./pkg/gateway -run 'ProjectSessionsList' -count=1` ok。

### Step 2：后端——其余项目标签接口的服务端分页

照 071 约定（不带 `page` 原样）：
| 接口 | `q` 匹配 | 筛选/排序 | 新字段 |
|---|---|---|---|
| `GET /api/rules/project/:projectId` | **只匹配文件路径**（“作用”列是前端按目录算的文案，服务端没有） | 原顺序 | — |
| `GET /api/v1/projects/:id/permissions/rules` | 工具名、规则内容 | 原顺序 | — |
| `GET /api/v1/projects/:id/lsp` | id、summary | 行 = 待确认 + 已允许 + 已拒绝 合成一张表（`status: pending/allowed/denied`，已允许/已拒绝的 `summary` 为空串），顺序 待确认 → 已允许 → 已拒绝；原 `pending/allowed/denied` 字段在不带 page 时照旧 | 带 page 时 `rows: [{id, summary, status}]` |
| `GET /api/v1/projects/:id/cron/jobs` | 名称、提示词 | 原顺序（创建时间） | — |
| `GET /api/v1/projects/:id/cron/jobs/:jobId/runs` | — | 开始时间新→旧 | 带 `page` 时用 `page/page_size` 取代 `limit`，返回 `runs` 一页 + `total`。存储层先补：`pkg/state/cron.go` 新增 `ListRunsPage(ctx, jobID string, limit, offset int) ([]CronRun, int, error)`（同一 WHERE 的 `COUNT(*)`，`limit` 上限 100），`pkg/process/cron_service.go` 新增 `JobRunsPage(ctx, agentID, jobID string, limit, offset int)`（权限校验照 `JobRuns`）；原函数不动 |
| `GET /api/v1/projects/:id/channels` | 通道 kind、显示名（中英文） | 顺序 = `channel.ChannelKinds` | 带 page 时 `rows: [{kind, label_zh, label_en, enabled, configured, fields, secret_keys, secret_hints}]`；不带 page 时仍返回原 `channels`/`kinds`，**不加**顶层 map（camelCase 会改 kind 键名） |

通道行字段说明：`fields` = 该 kind 的字段 map（已打码，同现有 GET）；`secret_keys` = 其中 `config.IsSecretLikeConfigKey` 为真的键列表；`secret_hints` = 每个**有值的**密钥键 → `providerKeyHint(s.Home, 原值)`（`••••` + 末 4 位，`${ENV}` 引用会读 `.env`，与模型服务同口径）；
`configured` = 库里**存有**该 kind 的行，且其 `ConfigJSON` 去空白后不是 `""`/`"{}"`（用户保存过配置）。
通道显示名表：**唯一数据源**是新建的 `frontend/src/lib/channelLabels.json`（`{"telegram": ["Telegram", "Telegram"], "feishu": ["飞书", "Feishu"], …}`，内容如下），Go 侧 `pkg/gateway/channel_labels.go` 写同样的 `map[string][2]string` 并加测试 `TestChannelLabelsMatchFrontendJSON` 读 `../../frontend/src/lib/channelLabels.json` 断言两者相等：
`telegram→Telegram/Telegram, discord→Discord/Discord, slack→Slack/Slack, whatsapp→WhatsApp/WhatsApp, signal→Signal/Signal, mattermost→Mattermost/Mattermost, matrix→Matrix/Matrix, homeassistant→Home Assistant/Home Assistant, email→邮件/Email, sms→短信/SMS, webhook→Webhook/Webhook, bluebubbles→BlueBubbles/BlueBubbles, wecom→企业微信/WeCom, wecom_callback→企业微信（回调）/WeCom callback, weixin→微信/WeChat, feishu→飞书/Feishu, dingtalk→钉钉/DingTalk, qq→QQ/QQ`
（这 18 个与 `pkg/channel/channel.go` 的 `ChannelKinds` 一一对应，已核对）。
每个接口一条分页用例 + 一条不带 page 用例；通道再加 `TestChannelsReportConfiguredOnlyForSavedKinds`（没存过的 kind `configured=false`）与 `TestChannelsPagedRowsCarrySecretHints`。

**Verify**：`CGO_ENABLED=1 go test -tags fts5 ./pkg/gateway -count=1` ok；`CGO_ENABLED=1 go test -tags fts5 ./pkg/state -run 'ListRunsPage' -count=1` ok；`gofmt -l pkg` 无输出。

### Step 3：前端——顶栏面包屑与标签

- **顶栏面包屑的 DOM 与类名**（072 执行后 `main.css` 里有 `.forebrain-topbar > :not(.forebrain-topbar-title):not(.forebrain-topbar-name):not(.forebrain-topbar-spacer) { flex: none }`，任何其它直接子元素都不能缩短）：
  1. 在 `main.css` 新增 `.forebrain-topbar-crumb { display: flex; align-items: center; gap: 6px; font-size: 14px; font-weight: 600; white-space: nowrap; min-width: 0; flex: 0 1 auto; } .forebrain-topbar-crumb > * { flex: none; } .forebrain-topbar-crumb > .forebrain-topbar-name { flex: 0 1 auto; min-width: 0; }`
     与 `.forebrain-topbar-meta { flex: 0 1 auto; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; font-size: 12px; color: var(--forebrain-muted-text); }`（原型 `.top .crumb`、`.top .meta`）；
     把上面那条 `flex: none` 规则的排除列表补上 `:not(.forebrain-topbar-crumb):not(.forebrain-topbar-meta)`。
  2. `ProjectDetailView.vue`：删页面里的 `.crumb` 行；`useTopbar().set({ leading: () => [...] })` 画三样：
     `<div class="forebrain-topbar-crumb">`（`RouterLink` “项目” → `/projects`，`ChevronRight`，`<span class="forebrain-topbar-name">{项目名}</span>` + `v-tip:bottom.trunc`）+ `<span class="forebrain-topbar-meta font-mono">{目录}</span>` + 信任徽标。
  3. `WorkshopTopbar.vue`：把 leading 的根元素（`<div v-if="part === 'leading'" class="flex min-w-0 items-center gap-2">`）的类改成 `forebrain-topbar-crumb`（072 执行后它被 `flex: none` 卡住，长技能名不能截断——这是 072 的遗留，在这里一并修），其余不动。
- 标签行样式改成原型 `.proj-head`（只剩标签：`padding: 4px 28px 0`，底边线）。标签文案 `projects.tabOverview` 改为 `项目设置` / `Project settings`。

**Verify**：`pnpm test` 全绿；Step 8 用例 1 与用例 8（长名称截断）通过。

### Step 4：前端——“项目设置”标签（`ProjectOverview.vue` 重写模板）

照“目标结构 · 项目设置”逐项实现：去掉三张 `CardComponent`；字段用现有 `forebrain-field` 与标签样式（12px、`--forebrain-text-2`）；记忆作用域用 071 的 `RadioField`；开关行照目标结构第 6 项；
信任开关读 `project.trusted`，保存时若变化则在 `projectUpdate` 里带 `trust`；项目指令随表单一起保存（`instructions`）；“置顶”从表单删除；
`删除项目`：**把 `ProjectsView.vue` 里删除预览 + `ConfirmDialog typedConfirm` 那段抽成 `components/project/ProjectDeleteDialog.vue`**（props `project`，emits `deleted`/`cancel`，内部自己调预览接口），`ProjectsView.vue` 改用它（行为不变），`ProjectOverview` 也用它，删除成功后 `router.push('/projects')`。
**保存成功后通知父组件刷新**：`ProjectOverview` `emit('saved')`；`ProjectDetailView.vue` 的 `<RouterView … @saved="load()" />`（`load` 是它现有的读项目函数），这样顶栏信任徽标、其它标签的信任门、本表单的“未改动”判断都会更新。
未改动时 `保存` 禁用；保存成功后提示沿用现有 `projects.saved`（灰字，显示在按钮行右侧）。

**Verify**：`ProjectOverview.test.ts`：字段顺序；保存载荷含 `instructions` 与（改了时）`trust`；保存后 emit `saved`；`删除项目` 打开 `ProjectDeleteDialog`。`ProjectsView.test.ts` 原有删除用例仍通过。`pnpm test` 全绿。

### Step 5：前端——规则指令、会话、记忆、权限、LSP、技能标签

每个标签：删 `PageTopRow`；`ListToolbar`（左搜索必有）+ 表格 + `TablePager` + `ListNote`（文案逐字照目标结构）；数据走各自接口的分页版本（`useServerList`）；信任门保留在最上面（未信任时只显示信任门）。
工具栏、搜索框、按钮、表格、分页器、说明的写法**照 073 已落地的 `views/ProjectsView.vue`**。搜索框高度用 `h-[34px]`（原型 `.field` 34px；073 的 `h-8` 由 073 Step 7 统一改）。
- 规则指令：删掉项目指令区块（已移到项目设置）。
- 会话：新表格（Step 1 的字段）；`打开` 与标题链接都进入该会话（现有跳转逻辑）；`新建项目会话` 保留现有行为；模型为空显示 `—`；心跳 `interval_seconds/60` 取整显示 `每 N 分钟`。
- 记忆：`ProjectMemory.vue` 只剩 `MemoryFilesPanel` + `ListNote`。
- 权限、LSP：照目标结构；LSP 的允许/拒绝调用现有 `projectLspConsent`。
- 技能：只删 `PageTopRow`、改 `ListNote` 文案（见目标结构）。

**Verify**：`grep -n 'PageTopRow' frontend/src/components/project/{ProjectRules,ProjectSessions,ProjectMemory,ProjectPerm,ProjectLsp,ProjectSkills}.vue` 无输出；`pnpm test` 全绿。

### Step 6：前端——定时任务与通道

- `lib/time.ts` 新增 `formatMonthDayTime(unixSeconds?: number): string` → 本地 `MM-DD HH:mm`，空值 `—`；单测。
- `CronView.vue`：按目标结构改成表格（`useServerList`）；启用开关调用现有暂停/恢复；`编辑` 打开现有编辑器；`执行记录` 打开新建的 `components/cron/CronRunsDialog.vue`（`ForebrainModal` + 表格 + `TablePager`，数据走执行记录分页接口；`打开对话` 沿用现有 `openConversation`；`查看输出` 行下展开 `output`/`error`，错误文案沿用 `runErrorText`）；
  删除原卡片里的内联执行记录。`ProjectCron.vue` 外层 `max-w-4xl` 改为与其它标签一致。
- `ProjectChannels.vue`：按目标结构改成表格；**通道接口的分页读与写都不走 camelCase 拦截器**——在 `lib/api.ts` 里用 `gatewayFetch` + `JSON.parse` 读原始 JSON 写 `projectChannelsPage(projectId, query)`（行字段手动映射：`kind, labelZh←label_zh, labelEn←label_en, enabled, configured, fields（原样，保持 snake_case 键）, secretKeys←secret_keys, secretHints←secret_hints（原样）`）；
  新建 `lib/channelLabels.ts`（`import labels from './channelLabels.json'`，按当前语言取中文或英文；不在表里的 kind 显示 kind 本身）；
  新建 `components/project/ChannelEditDialog.vue`：字段 = 该行 `fields` 的键（排除 `enabled`），标签 = 等宽键名；`secret_keys` 里的键是密钥：值为 `[REDACTED]` 时输入框显示为空、占位 `已保存，不修改即保留`，`type="password"`；有任何已存密钥时底部提示 `密钥已脱敏，不修改即保留原值。`。
  **保存与启用开关都必须整表写回**（PUT 会清掉 map 里没有的 kind）：先用**不带 page** 的原始读取拿到完整 `channels` map（同样用 `gatewayFetch` 读原始 JSON），在其上只改这一个 kind（密钥没改的保持 `[REDACTED]`），再整体 PUT——沿用现有 `saveProjectChannels(projectId, channels)`（它对 map 做 `toSnakeCase`，对已是 snake_case 的键原样输出，可以直接用）；成功后 `reload()`。删除页面级“保存”按钮与 `dirty` 状态。
  配置摘要：遍历该行 `fields` 里非空的键（排除 `enabled`），密钥键显示 `secret_hints[键]`，其余显示值。

**Verify**：`CronRunsDialog.test.ts`、`ChannelEditDialog.test.ts`（密钥框为空、提示出现、保存时 PUT 的 map 含**所有** kind 且未改的密钥是 `[REDACTED]`）、`channelLabels.test.ts`（18 个 kind 都有中英文名）、`time.test.ts` 新用例；`pnpm test` 全绿。

### Step 7：文案（zh/en）

新增/修改：`projects.tabOverview`（项目设置）、项目设置表单各标签与帮助（逐字照目标结构）、`projects.projectKey`（项目键）、`projects.trustThisDir`（信任此目录）、`projects.trustThisDirHint`、`projects.memoryScopeSharedHint`、`projects.memoryScopeProjectHint`、
各标签搜索占位与表下说明、会话表列名（标题/模型/消息/心跳/创建时间/更新时间/打开）、心跳 `每 {n} 分钟`/`已暂停`、定时任务列名（名称/计划/投递至/下次执行/最近一次/已执行/启用）与 `最近一次` 的三种状态、`执行记录 · {name}`、`查看输出`/`收起`、
通道列名（通道/投递标识/配置/启用）、`未配置`、`先编辑，填好配置`、`编辑通道 · {name}`、`已保存，不修改即保留`、`密钥已脱敏，不修改即保留原值。`、LSP 状态（待确认/已允许/已拒绝）。
**已存在的键只改值、不要重复添加**（对象里重复键会让 `vue-tsc` 报 TS1117）：先 `grep -n "'键名'" frontend/src/locales/index.ts` 确认。删除不再使用的键（`projects.overviewTitle`、`projects.dangerTitle`、`projects.deleteConfirm`、`channels.secretNotice`（若只在旧位置使用）、`rules.projectInstrTitle/Desc`）。

**Verify**：`cd frontend && npx vue-tsc --noEmit -p tsconfig.json` exit 0。

### Step 8：e2e `frontend/e2e/project-space-tables.spec.ts`（新建）+ 回归

造一个项目（含 2 个会话（一个有模型选择、一个有心跳）、1 个定时任务（有 3 条执行记录）、飞书已配置、Telegram 已配置、1 条权限规则），断言：
1. 顶栏左侧是 `项目 › {名称}` 面包屑 + 目录 + 信任徽标；页面里没有旧 `.crumb`；第一个标签文字 `项目设置`。
2. 项目设置：没有 `h2/h3`、没有卡片；字段顺序如目标结构；`记忆作用域` 是 `role=radiogroup`，点 `仅本项目` 帮助文字变；改项目指令保存后刷新仍在；关掉“信任此目录”保存后顶栏徽标变 `未信任`；`删除项目` 在 `保存` 右侧，点后出现输入项目名确认的弹窗。
3. 规则指令标签里没有 `project-instructions`；记忆标签里没有记忆作用域。
4. 会话：两行；`模型`、`消息`、`心跳` 列有值；点 `创建时间` 表头后 `aria-sort=descending`；分页器 `共 2 条`。
5. 定时任务：表格一行；启用开关关掉后 `下次执行` 显示 `已暂停`；`执行记录` 打开弹窗，表格 3 行、有分页器。
6. 通道：飞书行 `配置` 列有 `••••` + 末 4 位、开关可用；Slack 行 `未配置`、开关禁用；点飞书 `编辑` 弹窗标题 `编辑通道 · 飞书`、改 `app_id` 保存后表格刷新，**Telegram 的配置仍在**；页面没有“保存”按钮。
7. 每个标签内容区都没有 `h2`、第一行最左是 `input[type=search]`（项目设置除外）。
8. 把项目名改成 80 个字符：顶栏名称元素宽度 ≤ 360、`scrollWidth > clientWidth`，顶栏右侧按钮都在视口内。
全套回归：旧用例只允许因“面包屑位置、概览文案、卡片/标题行、文字按钮、通道页面级保存、项目指令/记忆作用域位置”改断言，逐条列在报告里。

**Verify**：`FOREBRAIN_E2E_SPEC=e2e/project-space-tables.spec.ts scripts/acceptance/web_e2e.sh` 末行 `web e2e: PASS`；全套 `scripts/acceptance/web_e2e.sh` 末行 `web e2e: PASS`。

## Test plan

Step 1/2 的 Go 用例（含 `TestChannelLabelsMatchFrontendJSON`）；`channelLabels.test.ts`、`ProjectOverview.test.ts`（字段顺序、保存载荷含 `instructions`/`trust`、删除弹窗）、`CronRunsDialog.test.ts`、`ChannelEditDialog.test.ts`；Step 8 e2e。
截图 `$TMPDIR/075-*.png`：项目设置、会话、定时任务、执行记录弹窗、通道、编辑通道弹窗，与原型并排对照。

## Done criteria

- [ ] Go：`./pkg/state`、`./pkg/gateway` 全过；`go vet`、`gofmt` 无输出
- [ ] 前端：`pnpm test` 全绿、`vue-tsc` exit 0
- [ ] `grep -rn 'PageTopRow\|CardComponent' frontend/src/components/project frontend/src/views/CronView.vue frontend/src/views/ProjectDetailView.vue` 无输出
- [ ] `grep -n 'window.confirm' frontend/src/components/project/ProjectOverview.vue` 无输出
- [ ] `project-space-tables.spec.ts` 与全套 `web_e2e.sh` → `web e2e: PASS`
- [ ] 与开工前相比（开工时先 `git status --short > $TMPDIR/075-before.txt`），新增或修改的文件都在 Scope 内（`git status --short | diff $TMPDIR/075-before.txt -` 里出现的路径逐个核对）

## STOP conditions

- Drift check 失败；071/073/074 未完成。
- Step 1.4 的查询计划断言失败（数消息走了全表扫描）——报告后再定是否加索引（加索引要走迁移）。
- 按 Step 6 先取全量再整体 PUT 之后，其它 kind 的配置或密钥仍然被清掉。
- `channel.ChannelKinds` 的实际值与显示名表对不上超过 3 个。
- 删除弹窗抽成共用组件后 `ProjectsView` 的删除流程行为有任何变化。

## Maintenance notes

- 项目级设置只有“项目设置”标签一个入口；新增项目级开关加在这张表单里，不要再做卡片。
- 通道显示名表前后端各一份（Go 用于搜索，前端用于显示），新增通道 kind 两边都补。
- 会话表的“消息数”口径（可见的 user/assistant 消息）若以后改动，要与对话视图的显示口径一致。
