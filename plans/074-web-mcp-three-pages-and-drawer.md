# Plan 074：MCP 三处页面——设置只管全局、菜单栏新增“MCP”页（全局 + 主代理）、项目空间 MCP 标签（三层）；统一表格、右侧抽屉查看/编辑/新增、服务端分页

> **执行者须知**：按步骤顺序执行，每步跑验证命令并确认预期结果后再进下一步。触发
> "STOP conditions" 时停下报告，不要即兴发挥。**不要更新 `plans/README.md`**（评审方维护索引）。
> **禁止提交代码**（owner 手动提交）：不建分支、不建 worktree、不 commit、不 push、不 stash；改动留在当前工作区。
> 先跑 `scripts/fetch-libtokenizers.sh`；`go test` 报 pdfium 找不到时先 `make pdfium pdfium-fonts` 并照 Makefile 导出 `PKG_CONFIG_PATH`。
>
> **前置**：plan 071 已完成（`ForebrainDrawer`、`TablePager`、`RowAction`、`ListToolbar`、`useServerList`、`list_page.go`）。
> **Drift check（先跑）**：以下每条都必须各有输出，否则 STOP：
> ```
> test -f frontend/src/components/common/ForebrainDrawer.vue && test -f pkg/gateway/list_page.go && echo ok
> grep -n 'func (s \*Server) handleMCPLayerList(' pkg/gateway/mcp_write_api.go
> grep -n 'func (s \*Server) handleProjectMCPPreview(' pkg/gateway/mcp_oauth_api.go
> grep -n 'agents.Post("/primary/:id/mcp/servers", s.handleMCPAgentServersWrite)' pkg/gateway/api_extra.go
> grep -n 'projects.Post("/:id/mcp/servers/test", s.handleProjectMCPServersTest)' pkg/gateway/api_extra.go
> grep -n "toggleDisabled(record)" frontend/src/components/settings/McpTab.vue
> ```

## Status

- **Priority**: P1
- **Effort**: L
- **Risk**: MED（新页面 + 三个读接口加分页/合并；写接口与连接测试**后端已全部存在**，本计划只接上前端）
- **Depends on**: plans/071-web-list-foundation.md
- **Category**: direction（设计稿落地）
- **Planned at**: commit `4888d76` + 工作区，2026-10-18

## Why this matters

owner 2026-10-14/17 定了 MCP 三层作用域在 web 上的页面范围（记忆 `mcp-page-scopes`）：
- **设置 · MCP**：只列、只增删改**全局** MCP；
- **菜单栏 MCP 页（新增）**：只列**全局 + 主代理**（不列项目级），只增删改主代理 MCP，全局行只读；**主代理的排在所有全局的前面**；
- **项目空间 · MCP 标签**：列**项目 + 主代理 + 全局**，只增删改项目 MCP，其余只读；**依次是项目、主代理、全局**；项目里新加、还没同意的行给“允许 / 拒绝”。
- 同名覆盖：项目 > 主代理 > 全局，整条覆盖；被覆盖的低层条目在该页**不显示**。
- 交互：只读行操作栏只有“查看”→ **右侧抽屉**只读表单（全部字段）、底部只有“取消”；可改的行“编辑”、以及“添加 MCP 服务”→ 右侧抽屉可编辑表单、底部“取消 / 保存”；**不用弹窗**。表格固定列宽、每格一行、下方分页。

后端在 2026-10-14（commit `1527c46`）已经有了三层的写接口与连接测试（`pkg/gateway/mcp_write_api.go`），但**前端从没接上**：
设置里的 `McpTab.vue` 仍是旧的只读卡片列表（只能“下个会话起禁用/启用”），菜单栏没有 MCP 页，项目 MCP 标签只能“允许/拒绝”。
owner 2026-10-18 裁决：所有列表服务端分页；原型没画的现有功能（OAuth 授权状态、官方标记、工具列表、“下个会话起禁用”）保留并按同一套规则放。

## 设计稿（验收依据，实施须 1:1 还原）

- 原型 https://claude.ai/artifact/KKfPW8dXWScuRdTzonsk2F（本地 `docs/plan/WEB_REDESIGN_AND_GATEWAY_ACCESS/app-preview.html`）。
  状态按钮：**设置 · MCP**、**MCP 页 · 全局 + 主代理**、**项目 · MCP 三层**、**MCP 抽屉 · 查看（只读）**、**MCP 抽屉 · 添加**、**MCP 抽屉 · 编辑（环境变量）**、**MCP 抽屉 · 编辑（请求头与 OAuth）**、**MCP 抽屉 · 测试连接**、**MCP 抽屉 · 连接失败**、**MCP 抽屉 · 名称重复**。
- 原型代码（照它实现，不要自己发挥）：`mcpEffective(layers)`、`mcpBlock(scope, title, rows, own, where, desc, layers)`、`mcpSrc`、`mcpType`、`mcpState`、`mcpStatusCell`、`mcpEnableCell`、`mcpActions`、
  `mcpOpen`、`mcpDrawer`、`mcpFormBody`、`mcpStdioAdv`、`mcpHTTPAdv`、`mcpStateBox`、`mcpEditElsewhere`、`mcpTestBar`、`mcpKV`、`mcpLabel`；CSS `.tb.mcp-tb`、`.mcp-name`、`.mcp-target`、`.mcp-ty`、`.mdw-state`、`.kvrows`、`.mcp-test*`、`.adv`。
- **表格**（`mcpBlock`）：工具栏左 `搜索 MCP 服务` + （多层页面才有）来源筛选 `全部来源/项目/主代理/全局`（只列本页出现的层，**高层在前**）；右 `添加 MCP 服务`（主色）、`刷新`。
  列（`table-layout: fixed`，单元格 `nowrap`、上下 10px 内边距）：MCP 服务 168px（等宽名称，`必需` 灰徽标）｜来源 84px（只有多层页面有：`项目` 黑底白字 / `主代理` 品牌浅底 / `全局` 灰底）｜命令或地址 自适应（等宽灰、截断、悬停全文）｜
  类型 108px（`stdio` / `http`，后面灰字 ` · OAuth` 或 ` · 请求头`）｜状态 148px（**只放一个徽标**：`已连接 · N 个工具` 绿 / `已允许 · N 个工具` 绿 / `待确认` 黄 / `已禁用` 灰（下个会话起，界面关的）/ `配置里已禁用` 灰（`enabled: false`）/ `连接失败` 红；失败原文、OAuth 状态放在徽标的悬停提示里）｜
  启用 56px（本页可改的行：开关 = “下个会话起启用/禁用”；`配置里已禁用` 的行开关禁用并提示 `配置里是 enabled: false：在编辑里打开“启用”`；待确认的行空着；只读行写 `—`）｜
  操作 88px（`mcpActions`：待确认 → `允许`（`check`，悬停绿）、`拒绝`（`x`，红）；本页可改 → `编辑`（`pencil`）、`删除`（`trash-2` 红）；只读 → `查看`（`eye`）+ 空位）。
  分页器；表下说明（逐字）：
  - 设置：`对所有主代理生效，新会话起作用。同名条目被主代理或项目整条覆盖。~/.forebrain/forebrain.yaml · agents.defaults.mcp_servers；改动对新会话生效。`
  - 菜单栏 MCP 页：`本主代理与全局的 MCP 服务，主代理的排在前面；同名时主代理覆盖全局。全局 MCP 在“设置 → MCP”修改，项目 MCP 在各项目空间；改动在新会话生效。`
  - 项目 MCP 标签：`本项目能加载的 MCP 服务，依次是本项目、主代理、全局的。同名时按 项目 → 主代理 → 全局 覆盖；这里只增删改项目 MCP，全局与主代理的在各自页面改。`
- **抽屉**（`mcpDrawer` + `mcpFormBody`）：宽 560px；标题 `添加 MCP 服务 · {全局/主代理/项目}` / `编辑 MCP 服务 · {name}` / `MCP 服务 · {name}`（查看时旁边灰徽标 `只读`）；
  正文：（编辑/查看时）状态框（来源、状态、失败原因；+ OAuth 授权状态、官方、工具列表、下个会话起禁用）→ 名称 `name` + 类型 `transport`（`stdio（本地命令）`/`http（远程地址）`）→ 命令或地址 →
  `必需`、`启用` 两个开关 → 连接超时（秒）`startup_timeout`、请求超时（秒）`request_timeout` → 折叠的“高级”（stdio：环境变量 KV、继承父进程环境 `inherit_parent_env`、命令 SHA256 校验 `expected_command_sha256`；
  http：请求头 KV、禁用独立 SSE `disable_standalone_sse`、OAuth（模式 `mode`、授权地址、令牌地址、client_id、client_secret、scopes、redirect_url））+ 默认工具审批模式 `default_tools_approval_mode` →
  配置文件位置（等宽灰字）→ 可编辑时“测试连接”行（`plug-zap` 图标；结果 `✓ 已连接` + `N 个工具 · x.x s` + `工具列表` 展开，或 `✗ 连接失败` + 原文）；只读时改为链接 `在“设置 → MCP”修改` / `在菜单栏“MCP”修改` / `在项目空间修改`。
  底部：查看 = 只有 `取消`；编辑/新增 = `取消` + `保存`（保存中显示 `保存中…` 并禁用）。保存失败（400/409）把服务端原文显示在表单底部的错误块里（`没有保存：` + 原文），抽屉不关。
  只读表单：所有输入 `readonly`、下拉与开关 `disabled`、空值显示占位 `未设置`；只读时不显示任何帮助文字（`mcpFormBody` 里 `ro ? '' : hint` 的那些）。
  帮助文字（可编辑时，逐字）：`必需 = 连接失败时本会话不启动；关闭“启用” = 配置里写 enabled: false，条目保留、不进新会话。`、
  `连接超时管启动并列出工具，请求超时管单次工具调用；都按秒，connectionTimeoutMs 这类外来拼写不做别名。`、
  `单工具的覆盖表（tools）这一轮不做界面，不提交就不会动它。`、`密钥值：[REDACTED] = 不修改；清空 = 删除这一项；密钥类键请写 ${ENV}。`

## Current state（行号为 `4888d76`，已核实）

后端（**写与测试已齐**）：
- 路由 `pkg/gateway/api_extra.go`：全局 `mcp.Get("/servers")`、`mcp.Post("/servers")`、`mcp.Post("/servers/test")`、`mcp.Put("/servers/:name")`、`mcp.Delete("/servers/:name")`、`/servers/disable`、`/servers/enable`、PKCE 两个（`:261-272`）；
  主代理 `agents.Post/Put/Delete("/primary/:id/mcp/servers[/:name]")`、`/primary/:id/mcp/servers/test`（`:90-93`）；
  项目 `projects.Get("/:id/mcp")`（预览）、`Post("/:id/mcp/consent")`、`Post/Put/Delete("/:id/mcp/servers[/:name]")`、`Post("/:id/mcp/servers/test")`（`:210-217`）。
- `mcp_write_api.go`：`mcpServerWrite`（写入载荷：`name`、`transport`（`stdio`/`streamable_http`）、`target`、`required`、`enabled`、…；**没送的字段不动**；`[REDACTED]` = 保留原密钥）；
  `readMCPServerWrite`（把配置条目读成同一形状，密钥打码）；`:864-898` `handleMCPLayerList`（`?scope=global|agent` 列出一层，含被高层覆盖的条目，行带 `running`）；`:945-1000` `mcpServerRow`（行字段：`scope`、`source`、`running`、`disabled_by_config`、`disabled_next_session`、`oauth_overlay`、`auth_status`、`required`、`tools`、`official_url`、`conn_status`、`tool_count`、`error`…）。
- `mcp_oauth_api.go:86-193` `handleMCPServersV1`：不带 `scope` 时返回运行中的有效列表（主 runner 或 `session_id` 指定的 runner），带 `scope=global|agent` 时走 `handleMCPLayerList`；
  `:312-375` `handleProjectMCPPreview`：项目视角的有效列表（`process.ResolveSessionMCP`，同名只留一条）+ `overridden_global` + `not_applied` + `pending_consent`（待同意的只在这个列表里，不在 `servers`）。
  三个读接口都**不分页**。
前端：
- `components/settings/McpTab.vue`：标题行 + 卡片列表（徽标一堆）+ “下个会话起禁用/启用”文字按钮（`toggleDisabled` → `mcpSetServerDisabled`）；**没有新增/编辑/删除/测试**。
- `components/project/ProjectMcp.vue`：标题行 + 信任门（未信任时只有“信任此项目”按钮，保留）+ 待同意列表（允许/拒绝文字按钮）+ 有效列表；**没有新增/编辑/删除/测试**。
- `App.vue:519-531` 菜单栏 `navItems` 没有 MCP；`router/index.ts` 没有 `/mcp`。
- `lib/api.ts:453-460` `mcpSetServerDisabled`；`:2308-2318` `mcpServers` / `mcpServersWithStatus`（`GET /v1/mcp/servers`）。

## Commands you will need

| 用途 | 命令 | 成功预期 |
|------|------|----------|
| gateway 包 | `CGO_ENABLED=1 go test -tags fts5 ./pkg/gateway -run 'MCP' -count=1` | ok |
| 全 gateway | `CGO_ENABLED=1 go test -tags fts5 ./pkg/gateway -count=1` | ok |
| vet / fmt | `go vet ./pkg/...` ；`gofmt -l pkg` | 无输出 |
| 前端 | `cd frontend && pnpm test` ；`npx vue-tsc --noEmit -p tsconfig.json` | 全绿 / exit 0 |
| 单个 e2e | `FOREBRAIN_E2E_SPEC=e2e/mcp-pages.spec.ts scripts/acceptance/web_e2e.sh` | `web e2e: PASS` |
| 全套 e2e | `scripts/acceptance/web_e2e.sh` | `web e2e: PASS` |

## Scope

**In scope**：`pkg/gateway/mcp_oauth_api.go`、`pkg/gateway/mcp_write_api.go`（只加读的分支/辅助，不改写入语义）、对应测试；
`frontend/src/components/mcp/`（新建目录：`McpServersTable.vue`、`McpServerDrawer.vue`、`McpKeyValueRows.vue`、`mcpRows.ts` + 测试）、`frontend/src/views/McpView.vue`（新建）、
`frontend/src/router/index.ts`（加 `/mcp`）、`frontend/src/App.vue`（`navItems` 加 MCP，位置在“技能”之后、“子代理”之前，图标 `Plug`）、
`frontend/src/components/settings/McpTab.vue`（整体换成 `McpServersTable view="global"`）、`frontend/src/components/project/ProjectMcp.vue`（信任门保留，其下换成 `McpServersTable view="project"`）、
`frontend/src/lib/api.ts`（MCP 读写方法）、`frontend/src/locales/index.ts`、`frontend/e2e/mcp-pages.spec.ts`（新建）及因旧 MCP 界面断言而必须更新的既有 e2e（只改断言）。

**Out of scope**：MCP 的写入语义、连接测试实现、PKCE 授权向导的后端（只在抽屉里保留一个“授权”入口，见 Step 6）；单工具审批覆盖表（`tools`，设计稿明确这一轮不做界面）；工作台概览里的 MCP 状态（`OverviewTab.vue`）。

## Steps

### Step 1：后端——三个读视图 + 服务端分页

在 `handleMCPServersV1` 里新增两种读法，项目预览加分页（都遵守 071：**不带 `page` 原样回答**；带 `page` 时 `q`（名称、命令/地址）→ `source`（逗号分隔：`project,agent,global`）→ 固定顺序 → 切页，保留 `servers` 键 + `total/page/page_size`）：
1. `GET /api/v1/mcp/servers?scope=global&page=…`：现有 `handleMCPLayerList` 的结果切页（设置页用）。顺序 = 配置文件里的顺序。
2. `GET /api/v1/mcp/servers?view=agent&page=…`（**新**，菜单栏 MCP 页用）：主 runner 的 `lc.Agents.MCPServersFor(agent)`（主代理层）与 `lc.Agents.Defaults.MCPServers`（全局层）合并：
   **同名时只留主代理层**；顺序 = 主代理层（配置顺序）在前、全局层在后；每行用现有 `mcpServerRow(rc, srv, scope, source, running, entry)` 渲染（`entry` 取 `rc.inv.Entry(name)` 且 `eff.Scope == scope` 时才给），所以运行状态与设置页一致。
3. `GET /api/v1/projects/:id/mcp?page=…`（项目预览加分页）：`servers` 改为**固定顺序** 项目 → 主代理 → 全局（同名已由 `ResolveSessionMCP` 去重）；
   把 `pending_consent` 里的每一项也作为一行并入（`scope: "project"`、`pending: true`、`summary`、`name`，其余字段取项目配置文件里该条目的 `readMCPServerWrite`，取不到就只带名字），排在项目层**最前面**；
   带 `page` 时 `total` 计入这些待同意行；`overridden_global`、`not_applied`、`pending_consent` 三个原字段照旧返回（顶层 extra）。
   项目层行的状态：已同意 → `allowed: true`；主代理/全局层行的运行状态取主 runner 的 `rc`（与菜单栏 MCP 页同口径）。
测试（`api_extra_test.go` 或新建 `mcp_read_api_test.go`，照现有 MCP 测试的配置造法）：
- `TestMCPAgentViewHidesShadowedGlobalAndListsAgentFirst`：全局 a、b，主代理 b、c → 行顺序 b(agent) c(agent) a(global)，没有全局 b。
- `TestMCPLayerListPagesWithTotal`：全局 12 条，`page=2&page_size=10` → 2 行、`total=12`；`q` 只匹配名称/命令。
- `TestProjectMCPPreviewOrdersProjectAgentGlobalAndIncludesPending`：项目 1 条已同意 + 1 条待同意、主代理 1 条、全局 1 条 → 顺序 待同意、项目、主代理、全局；`source=global` 只剩全局行。
- `TestProjectMCPPreviewWithoutPageKeepsOldFields`：不带 page 时 `overridden_global`/`not_applied`/`pending_consent` 仍在。

**Verify**：`CGO_ENABLED=1 go test -tags fts5 ./pkg/gateway -run 'MCP' -count=1` ok；`gofmt -l pkg/gateway` 无输出。

### Step 2：前端 API

`lib/api.ts` 新增（类型照 `mcpServerRow`/`readMCPServerWrite` 的字段，camelCase）：
- `McpServerRow`（`name, transport, target, required, enabled, startupTimeout, requestTimeout, headers, env, inheritParentEnv, expectedCommandSha256, disableStandaloneSse, oauth, defaultToolsApprovalMode, scope, source, running, disabledByConfig, disabledNextSession, authStatus, tools, officialUrl, connStatus, toolCount, error, pending?, summary?, allowed?`——以 `readMCPServerWrite` 实际输出为准，先打印一次真实响应核对键名，**以真实响应为准**）。
- `mcpPage(view: 'global' | 'agent' | 'project', query: ListQuery & { projectId?: string; source?: string })` → `{ rows, total, page, pageSize }`（`global` → `GET /v1/mcp/servers?scope=global`，`agent` → `?view=agent`，`project` → `GET /v1/projects/:id/mcp`）。
- 写：`mcpCreate(scope, projectIdOrAgentId, body)`、`mcpUpdate(scope, id, name, body)`、`mcpDelete(scope, id, name)`、`mcpTest(scope, id, body)`——三层分别打到全局 / `/agents/primary/:id/...` / `/v1/projects/:id/...`；载荷字段 snake_case，**只送用户改过的字段 + 必要的 name/transport**（后端“没送的字段不动”）。
- 已有 `mcpSetServerDisabled`（启用开关用）、项目同意 `POST /v1/projects/:id/mcp/consent`（找现有方法名复用）。

### Step 3：`components/mcp/mcpRows.ts`（纯函数，带单测）

- `mcpStatus(row)` → `{ tone: 'ok'|'warn'|'gray'|'err', label: string, tip: string }`，规则（与原型 `mcpState`/`mcpStatusCell` 一致）：
  `pending` → 待确认(warn)；`disabledByConfig` → 配置里已禁用(gray, tip `enabled: false`)；`disabledNextSession` → 已禁用(gray, tip `下个会话起不加载`)；
  `connStatus === 'failed'` 或有 `error` → 连接失败(err, tip = 原文)；项目层已同意 → `已允许 · N 个工具`(ok)；`running` → `已连接 · N 个工具`(ok；N 取 `toolCount`，为 0 或未知时不带后缀)；其余 → 未运行(gray)。
  OAuth：`authStatus` 为 `needs-auth`/`failed` 时把 `OAuth 需要授权` / `OAuth 授权失败` 追加进 tip（+，保留现有功能）。
- `mcpTypeLabel(row)` → `{ main: 'stdio' | 'http', sub: 'OAuth' | '请求头' | '' }`。
- `mcpEditPlace(scope)` → `{ to, label }`：项目 → 项目空间 MCP 标签 `在项目空间修改`；全局 → `/settings?tab=mcp` `在“设置 → MCP”修改`；主代理 → `/mcp` `在菜单栏“MCP”修改`。
- `mcpWritePayload(original, form)` → 只含变化字段的载荷（新增时全量）；KV（env/headers）按键合并：值未动的密钥键送 `[REDACTED]`、删除的键送空串。

### Step 4：`McpServersTable.vue`

props：`view: 'global' | 'agent' | 'project'`、`projectId?: string`、`agentId: string`（当前主代理 id，主代理层写接口要用）。
- `layers` = global → `['global']`；agent → `['agent','global']`；project → `['project','agent','global']`（显示顺序，高层在前）；`own` = 本页可改的层（global→global，agent→agent，project→project）。
- 数据 `useServerList((q) => mcpPage(view, { ...q, projectId, source }))`；来源筛选只在多层页面出现。
- 表格结构、列宽、单元格、操作按钮完全照“设计稿 · 表格”。启用开关调用 `mcpSetServerDisabled(name, !on)` 后 `reload()`；允许/拒绝调用项目同意接口后 `reload()`；删除先弹 `ConfirmDialog`（`删除 MCP 服务 {name}？`，危险样式）再调 `mcpDelete`。
- 打开抽屉：`查看`/`编辑`/`添加 MCP 服务` → `McpServerDrawer`（`mode`、`scope`、`row`）。保存成功后关抽屉并 `reload()`。
- 工具栏、分页器、表下说明用 071 的件；说明文案按 view 取（见设计稿）。

### Step 5：`McpServerDrawer.vue` + `McpKeyValueRows.vue`

- 外壳用 `ForebrainDrawer`（`:readonly="mode === 'view'"`）。正文照“设计稿 · 抽屉”逐项实现（字段顺序、键名小字 `.key` 灰色等宽、帮助文字逐字）。
- `McpKeyValueRows`：一行一对（键、值两个等宽输入 + 删除图标按钮 `v-tip:top="删除"`），下方 `添加环境变量` / `添加请求头` 按钮（`justify-self: start`，不要撑满）；只读时没有删除与添加，空时写 `未设置`。
- 类型下拉切换 stdio/http 时，高级区跟着换（原型 `data-mcptype`）。
- “测试连接”：调用 `mcpTest(scope, id, 当前表单载荷)`；显示 `测试中…` → 成功 `✓ 已连接` 绿徽标 + `N 个工具 · x.x s` + `工具列表`/`收起工具` 链接（展开为等宽小标签列表）；失败 `✗ 连接失败` 红徽标 + 原文（等宽、可折行）。测试不写配置、不关抽屉。
- 保存：新增 `mcpCreate`、编辑 `mcpUpdate`；按钮 `保存中…`；失败时表单底部错误块：`没有保存：` + 服务端原文（409 同名、400 校验）。
- 只读时状态框下方多出（+）：OAuth 授权状态、官方标记、工具列表（可折叠）、“下个会话起不加载”；OAuth 授权状态只显示文字（`需要授权` / `授权失败` / `已授权`）：**现状核实，前端没有任何 PKCE 调用**（`grep -rln pkce frontend/src` 为空），本计划不新做授权向导，报告里写明。
- 关闭：Esc、点遮罩、`取消`、右上 X；保存中不允许关闭。

### Step 6：三处页面接上

- `McpTab.vue`：整体替换为 `<McpServersTable view="global" :agent-id />`（标题行删除；设置页其它布局留给 076）。
- 新建 `views/McpView.vue`：`<McpServersTable view="agent" :agent-id="activePrimaryId" />`，外层滚动容器与其它菜单栏页面一致（`max-w-5xl` 一类，照 `ProjectsView.vue` 的外层）。
  `router/index.ts` 加 `{ path: '/mcp', name: 'mcp', component: () => import('@/views/McpView.vue'), meta: { title: () => t('routes.mcpTitle') } }`；`App.vue` `navItems` 在技能后插 `{ to: '/mcp', label: L.mcp, icon: Plug, scope: 'agent' }`（`locales/index.ts:2814` 的 `navLabels()` 加 `mcp: 'MCP'`，`routes.mcpTitle` 也加）。
- `ProjectMcp.vue`：保留信任门（未信任时只显示信任提示与按钮）；已信任时显示 `<McpServersTable view="project" :project-id :agent-id />`；删掉旧的待同意列表与有效列表（已并入表格）与标题行。

### Step 7：e2e `frontend/e2e/mcp-pages.spec.ts`（新建）+ 回归

造配置：全局 `a`、`b`，主代理 `b`、`c`，项目 `d`（已同意）、`e`（待同意）。断言：
1. 设置 · MCP：只有全局两行（`a`、`b`），没有来源列；`编辑 b` 打开抽屉、标题 `编辑 MCP 服务 · b`、底部 `取消`+`保存`；改请求超时保存后列表刷新。
2. 菜单栏出现 `MCP`；页面行顺序 `b(主代理) c(主代理) a(全局)`；全局行操作只有 `查看`，抽屉只读（没有可编辑的 input、底部只有 `取消`）、有 `在“设置 → MCP”修改` 链接。
3. 项目 MCP 标签：顺序 `e(待确认) d(项目) b c(主代理) a(全局)`；`e` 行操作是 `允许`/`拒绝`；点 `允许` 后状态变 `已允许`。
4. `添加 MCP 服务`（项目）→ 抽屉标题 `添加 MCP 服务 · 项目`；名称填已存在的 `d` 保存 → 抽屉不关、底部错误块含 `没有保存：`。
5. 测试连接：对一个本地不存在的 http 地址测试 → 出现 `✗ 连接失败` 与原文。
6. 每页都有分页器；全局造 12 条后设置页显示 `共 12 条`、2 个页码。
全套回归：旧用例只允许因“旧 MCP 卡片/文字按钮/标题行”改断言，逐条列在报告里。

## Test plan

Step 1 的 Go 用例；`mcpRows.test.ts`（状态、类型、载荷差分）；`McpServerDrawer.test.ts`（只读时无可编辑控件、保存失败显示错误块、测试结果三态）；`McpServersTable.test.ts`（三种 view 的列与操作按钮）；Step 7 e2e。
截图 `$TMPDIR/074-*.png`：三处页面、查看抽屉、编辑（环境变量）、编辑（OAuth）、测试成功/失败、名称重复，各与原型对应状态并排对照。

## Done criteria

- [ ] `CGO_ENABLED=1 go test -tags fts5 ./pkg/gateway -count=1` ok；`go vet`、`gofmt` 无输出
- [ ] `pnpm test` 全绿、`vue-tsc` exit 0
- [ ] `mcp-pages.spec.ts` 与全套 `web_e2e.sh` → `web e2e: PASS`
- [ ] `grep -n 'PageTopRow' frontend/src/components/settings/McpTab.vue frontend/src/components/project/ProjectMcp.vue` 无输出
- [ ] `git status` 只出现 Scope 列出的文件

## STOP conditions

- Drift check 失败；071 未完成。
- 真实响应的行字段与 `readMCPServerWrite`/`mcpServerRow` 不一致到无法映射（例如没有 `transport`）。
- 主代理层写接口要求的 `:id` 不是当前主代理 id（或切换主代理后写到了别的主代理）。
- 项目预览里拿不到待同意条目的配置内容，且没有它们表格就无法显示命令/类型——报告后再定是否只显示名称。
- 需要改写入语义（`mcpServerWrite` 的合并规则）才能让表单保存成功。

## Maintenance notes

- 三处页面共用 `McpServersTable` / `McpServerDrawer`，差别只在 `view`；新增一层或改覆盖规则时同时改后端三个读视图与 `layers/own`。
- 表格状态列只放一个徽标，细节（失败原文、OAuth、`enabled: false`）一律放悬停提示和抽屉状态框——不要再往表格里加徽标。
- 写载荷只送变化字段，依赖后端“没送的字段不动、`[REDACTED]` 保留原值”的约定；改后端合并规则必须同步改 `mcpWritePayload`。
