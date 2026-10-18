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
- `lib/api.ts:1408-1411` 响应拦截器把**所有**响应深层转成 camelCase（`lib/case.ts`），`env`/`headers` 这种**用户自定义键**的对象也会被改名——MCP 读写必须绕开它（Step 2）。
- `pkg/process/mcp.go:205-226` `DecideProjectMCPConsents`：把所有未决定的项目条目一次决定完（不在 `allowed` 里的记拒绝）；现有项目 MCP 页的 `consent(name, allow)`（`ProjectMcp.vue:132-144`）就是 `projectMcpConsent(id, allow ? [name] : [])`，所以点一行会顺带拒绝其它待确认行。
- `pkg/mcp/registry.go:15-26` 连接状态取值：`unknown/connecting/connected/disconnected/error/cancelled`。`mcp_write_api.go:988` 行字段 `pending` = 本代 MCP 启动尚未完成（不是“待同意”）。

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

**In scope**：`pkg/gateway/mcp_oauth_api.go`、`pkg/gateway/mcp_write_api.go`（只加读的分支/辅助，不改写入语义）、`pkg/gateway/mcp_read_api_test.go`（新建）、`pkg/process/mcp.go`（只加 `DecideProjectMCPConsentsFor`）与 `pkg/process/mcp_test.go`；
`frontend/src/components/mcp/`（新建目录：`McpServersTable.vue`、`McpServerDrawer.vue`、`McpKeyValueRows.vue`、`mcpRows.ts` + 测试）、`frontend/src/views/McpView.vue`（新建）、
`frontend/src/router/index.ts`（加 `/mcp`）、`frontend/src/App.vue`（`navItems` 加 MCP，位置在“技能”之后、“子代理”之前，图标 `Plug`）、
`frontend/src/components/settings/McpTab.vue`（整体换成 `McpServersTable view="global"`）、`frontend/src/components/project/ProjectMcp.vue`（信任门保留，其下换成 `McpServersTable view="project"`）、
`frontend/src/lib/api.ts`（MCP 读写方法；`projectMcpConsent` 加可选 `decide`）与 `frontend/src/lib/api.test.ts`、`frontend/src/locales/index.ts`、`frontend/e2e/mcp-pages.spec.ts`（新建）及因旧 MCP 界面断言而必须更新的既有 e2e（只改断言）。

**Out of scope**：MCP 的写入语义、连接测试实现、PKCE 授权向导的后端（只在抽屉里保留一个“授权”入口，见 Step 6）；单工具审批覆盖表（`tools`，设计稿明确这一轮不做界面）；工作台概览里的 MCP 状态（`OverviewTab.vue`）。

## Steps

### Step 1：后端——三个读视图 + 服务端分页 + 只决定一条的同意

三个读视图都遵守 071：**不带 `page` 原样回答**；带 `page` 时 `q`（名称、命令/地址，不分大小写包含）→ `layer` 筛选 → 固定顺序 → 切页，保留 `servers` 键 + `total/page/page_size`。
**按层筛选的参数叫 `layer`**（逗号分隔，取值 `project,agent,global`），按**行的 `scope` 字段**过滤——注意行里本来就有一个 `source` 字段（配置来源路径，`mcp_write_api.go:948`），不要拿它过滤，也不要把查询参数叫 `source`。
1. `GET /api/v1/mcp/servers?scope=global&page=…`：现有 `handleMCPLayerList` 的结果切页（设置页用）。顺序 = 配置文件里的顺序。
   每行**新增** `shadowed_by`：`rc.inv.Entry(name)` 存在且其 `Scope` 不等于本层时取那个 `Scope`（`agent` 或 `project`），否则空串（原型设置页“未运行 · 被覆盖”要用）。
2. `GET /api/v1/mcp/servers?view=agent&page=…`（**新**，菜单栏 MCP 页用）：主 runner 的 `lc.Agents.MCPServersFor(agent)`（主代理层）与 `lc.Agents.Defaults.MCPServers`（全局层）合并：
   **同名时只留主代理层**；顺序 = 主代理层（配置顺序）在前、全局层在后；每行用现有 `mcpServerRow(rc, srv, scope, source, running, entry)` 渲染（`entry` 取 `rc.inv.Entry(name)` 且 `eff.Scope == scope` 时才给），所以运行状态与设置页一致。
3. `GET /api/v1/projects/:id/mcp?page=…`（项目预览加分页）：`servers` 改为**固定顺序** 项目 → 主代理 → 全局（同名已由 `ResolveSessionMCP` 去重）；
   把 `pending_consent` 里的每一项也作为一行并入，键 **`consent_pending: true`**（**不要用 `pending`**——`mcpServerRow` 已用 `pending` 表示“这一代 MCP 启动尚未完成”，`mcp_write_api.go:988`）+ `scope: "project"`、`summary`、`name`，其余字段取项目配置文件里该条目的 `readMCPServerWrite`（取不到就只带名字），排在项目层**最前面**；
   带 `page` 时 `total` 计入这些待同意行；`overridden_global`、`not_applied`、`pending_consent` 三个原字段照旧返回（顶层）。
   项目层已同意的行加 `consent_allowed: true`；主代理/全局层行的运行状态取主 runner 的 `rc`（与菜单栏 MCP 页同口径）。
4. **同意接口改成可以只决定一条**（**owner 2026-10-18 待确认，见 README**）：现状 `pkg/process/mcp.go:205-226` `DecideProjectMCPConsents(workspace, launch, allowed)` 会把**所有**未决定的条目都记下来——不在 `allowed` 里的一律记成拒绝，
   所以在网页上点某一行“允许”会把其它待确认行一起拒掉。改法：`POST /v1/projects/:id/mcp/consent` 的 body 增加可选 `decide: ["name", …]`；**有 `decide` 时只处理这些名字**（在 `allow` 里 → 允许，否则拒绝），其余待确认条目保持未决定；没有 `decide` 时行为不变（终端启动提示仍是“一次决定全部”）。
   在 `pkg/process/mcp.go` 加 `DecideProjectMCPConsentsFor(workspace, launch, decide, allowed []string) error`（只遍历 `decide` 里的名字），`handleProjectMCPConsent` 按 body 选择调用哪个。
测试（新建 `pkg/gateway/mcp_read_api_test.go`，配置造法照 `api_extra_test.go` 里现有 MCP 写接口测试；同意的测试放 `pkg/process/mcp_test.go`）：
- `TestMCPAgentViewHidesShadowedGlobalAndListsAgentFirst`：全局 a、b，主代理 b、c → 行顺序 b(agent) c(agent) a(global)，没有全局 b。
- `TestMCPLayerListPagesWithTotalAndShadowedBy`：全局 12 条（其中 1 条被主代理同名覆盖），`page=2&page_size=10` → 2 行、`total=12`；被覆盖那行 `shadowed_by=agent`；`q` 只匹配名称/命令。
- `TestProjectMCPPreviewOrdersProjectAgentGlobalAndIncludesPending`：项目 1 条已同意 + 1 条待同意、主代理 1 条、全局 1 条 → 顺序 待同意、项目、主代理、全局；待同意行 `consent_pending=true` 且**没有** `pending` 键被误设；`layer=global` 只剩全局行。
- `TestProjectMCPPreviewWithoutPageKeepsOldFields`：不带 page 时 `overridden_global`/`not_applied`/`pending_consent` 仍在。
- `TestDecideProjectMCPConsentsForLeavesOthersUndecided`：两条待确认，只决定第一条 → 第二条仍在 `PendingProjectMCPConsents` 里。

**Verify**：`CGO_ENABLED=1 go test -tags fts5 ./pkg/gateway -run 'MCP' -count=1` ok；`CGO_ENABLED=1 go test -tags fts5 ./pkg/process -run 'Consent' -count=1` ok；`gofmt -l pkg` 无输出。

### Step 2：前端 API——MCP 的读写**不走**驼峰转换

**关键**：`lib/api.ts:1408-1411` 的响应拦截器对所有响应做**深层** camelCase（`lib/case.ts` `camelcaseKeys(…, { deep: true })`），会把 `env`/`headers` 里的**用户自己的键**改掉（`CODEGRAPH_HOME` → `codegraphHome`、`Authorization` → `authorization`），保存时再 snake 回去就写坏用户配置。
所以 MCP 的读写方法**全部用 `gatewayFetch`**（`api.ts` 内的模块函数，带会话 cookie 与 401 处理）读写**原始 JSON**：
- 读：`JSON.parse` 后用手写的 `toMcpRow(raw)` 把**行的顶层字段**逐个映射成 camelCase（`name, transport, target, required, enabled, startup_timeout→startupTimeout, request_timeout→requestTimeout, headers, env, inherit_parent_env, expected_command_sha256, disable_standalone_sse, oauth, default_tools_approval_mode, scope, source, running, disabled_by_config, disabled_next_session, auth_status, tools, official_url, conn_status, tool_count, error, shadowed_by, consent_pending, consent_allowed, summary`），
  **`env` 与 `headers` 原样复制，`oauth` 只把它自己的顶层键映射**；先 `console.log` 一次真实响应核对键名，**以真实响应为准**。`total/page/page_size` 照 071 的 `ListPageMeta` 映射。
- 写：载荷**手写 snake_case**，`env`/`headers` 原样发送，不经过 `toSnakeCase`。
- 方法：`mcpPage(view: 'global' | 'agent' | 'project', query: ListQuery & { projectId?: string })`（`layer` 走 `query` 的筛选参数）→ `{ rows, total, page, pageSize }`（`global` → `GET /api/v1/mcp/servers?scope=global`，`agent` → `?view=agent`，`project` → `GET /api/v1/projects/:id/mcp`）；
  `mcpCreate(scope, ownerId, body)`、`mcpUpdate(scope, ownerId, name, body)`、`mcpDelete(scope, ownerId, name)`、`mcpTest(scope, ownerId, body)`——`ownerId` 在全局时忽略、主代理时是主代理 id（打到 `/api/agents/primary/:id/mcp/servers…`）、项目时是项目 id（`/api/v1/projects/:id/mcp/servers…`）。
- 已有方法复用：启用开关 `mcpSetServerDisabled(name, disabled, sessionId?)`（`api.ts:453`）；项目同意 `projectMcpConsent(id, allow)`（`api.ts:2429`）——**加第三个可选参数 `decide?: string[]`** 透传 Step 1.4 的新字段。

**Verify**：`lib/api.test.ts` 加一条用例：mock 一个含 `env: { CODEGRAPH_HOME: 'x' }`、`headers: { Authorization: 'y' }` 的响应，`mcpPage` 读出后键名不变；`mcpUpdate` 发出的 body 里键名也不变。`cd frontend && pnpm test -- src/lib/api.test.ts` 通过。

### Step 3：`components/mcp/mcpRows.ts`（纯函数，带单测）

- `mcpStatus(row)` → `{ tone: 'ok'|'warn'|'gray'|'err', label: string, tip: string }`，按顺序判定（与原型 `mcpState`/`mcpStatusCell` 一致）：
  1. `consentPending` → `待确认`(warn)；
  2. `shadowedBy` 非空 → `未运行 · 被覆盖`(gray)，tip `被{主代理/项目}覆盖，未运行`；
  3. `disabledByConfig` → `配置里已禁用`(gray)，tip `enabled: false`；
  4. `disabledNextSession` → `已禁用`(gray)，tip `下个会话起不加载`；
  5. `connStatus === 'error'` 或有 `error` → `连接失败`(err)，tip = 原文；
  6. `consentAllowed` → `已允许 · N 个工具`(ok)；
  7. `connStatus === 'connecting'` → `连接中`(warn)；
  8. `running` 且 `connStatus === 'connected'`（或运行中但还没有状态）→ `已连接 · N 个工具`(ok；N 取 `toolCount`，0 或未知时不带 ` · N 个工具`)；
  9. 其余（`unknown`、`disconnected`、`cancelled`、未运行）→ `未运行`(gray)。
  连接状态的全部取值见 `pkg/mcp/registry.go:15-26`：`unknown/connecting/connected/disconnected/error/cancelled`（**没有** `failed`）。
  OAuth：`authStatus` 为 `needs-auth`/`failed` 时在 tip 末尾追加 `；OAuth 需要授权` / `；OAuth 授权失败`（+，保留现有功能）。
- `mcpTypeLabel(row)` → `{ main: 'stdio' | 'http', sub: 'OAuth' | '请求头' | '' }`（`transport === 'streamable_http'` 显示 `http`；有 oauth 模式 → `OAuth`，否则有 headers → `请求头`）。
- `mcpEditPlace(scope)` → `{ to, label }`：项目 → 项目空间 MCP 标签 `在项目空间修改`；全局 → `/settings?tab=mcp` `在“设置 → MCP”修改`；主代理 → `/mcp` `在菜单栏“MCP”修改`。
- `mcpWritePayload(original | null, form)` → 写载荷（snake_case），规则：
  - 新增：全量。编辑：只放变化的标量字段 + 路由要求的 `name`/`transport`。
  - `transport`：下拉的 value 用 `stdio` / `streamable_http`（文案 `stdio（本地命令）` / `http（远程地址）`）。
  - 超时输入框**清空 → 发 `0`**（后端 0 = 用默认/不限制；不发 = 不改）。
  - `default_tools_approval_mode`：`跟随全局（留空）` → 发空串。
  - `env`/`headers`（KV）：按键合并——值没动的**密钥**键发 `[REDACTED]`、被删的键发空串、新键/改过的键发新值；键名原样。
  - `oauth`：后端是**整块替换**（`mcp_write_api.go:114`、`:409-422`），所以只要 OAuth 区任何字段改了，就发**完整** `oauth` 对象：表单没显示的字段（`access_token`、`token_type`、`refresh_token`）从原值回填，密钥字段未改时回填 `[REDACTED]`；`scopes` 输入框按空白拆成数组；模式 `不启用（留空）` 发空串 `mode`。OAuth 区没改动就**不发** `oauth`。

**Verify**：`mcpRows.test.ts` 覆盖上面 9 种状态各一例、`streamable_http` 显示 `http`、超时清空发 0、OAuth 改一个字段时整块回填、大写 env 键往返不变；`cd frontend && pnpm test -- src/components/mcp` 通过。

### Step 4：`McpServersTable.vue`

props：`view: 'global' | 'agent' | 'project'`、`projectId?: string`、`agentId: string`（当前主代理 id，主代理层写接口要用）。
- `layers` = global → `['global']`；agent → `['agent','global']`；project → `['project','agent','global']`（显示顺序，高层在前）；`own` = 本页可改的层（global→global，agent→agent，project→project）。
- 数据 `useServerList((q) => mcpPage(view, { ...q, projectId }), { filters: { layer: '' } })`——来源下拉绑 `filters.layer`（换来源会回到第 1 页）；来源下拉只在多层页面出现，选项按 `layers` 顺序（`全部来源` + 中文层名）。
- 工具栏、表格、分页器、表下说明的写法**照 073 已落地的 `frontend/src/views/ProjectsView.vue`**（`ListToolbar` + 搜索框 `forebrain-field h-[34px]`、`max-width: 240px`（原型 `.field` 34px；073 落地的 `h-8` 是 32px，按 073 Step 7 统一改）+ 右侧按钮；`TablePager`；`ListNote`）；说明文案按 view 取（见设计稿）。
- 表格结构、列宽、单元格、操作按钮完全照“设计稿 · 表格”，状态列用 `mcpStatus`（徽标 + `v-tip:top` = tip，tip 为空不加）。
- 启用开关：`checked = !row.disabledNextSession && !row.disabledByConfig`；`disabledByConfig` 的行开关 `disabled` 并 `v-tip:top="配置里是 enabled: false：在编辑里打开“启用”"`；`consentPending` 的行不画开关；只读行写 `—`。
  切换：`await mcpSetServerDisabled(row.name, !nextChecked)` 后 `reload()`。
- 允许/拒绝（只在 `consentPending` 的行）：允许 `projectMcpConsent(projectId, [name], [name])`、拒绝 `projectMcpConsent(projectId, [], [name])`（第三个参数是 Step 1.4 的 `decide`）；完成后 `reload()`。
- 删除：先弹 `ConfirmDialog`（标题 `删除 MCP 服务 {name}？`，`danger`）再调 `mcpDelete`，完成后 `reload()`。
- 打开抽屉：`查看`/`编辑`/`添加 MCP 服务` → `McpServerDrawer`（`mode`、`scope`、`row`、`ownerId`）。保存成功后关抽屉并 `reload()`。

**Verify**：`McpServersTable.test.ts`：三种 view 的表头（单层没有“来源”列）；自有行操作是 编辑/删除、他层行是 查看+空位、`consentPending` 行是 允许/拒绝；`disabledByConfig` 行开关禁用；`pnpm test -- src/components/mcp` 通过。

### Step 5：`McpServerDrawer.vue` + `McpKeyValueRows.vue`

- 外壳用 071 的 `ForebrainDrawer`（`:readonly="mode === 'view'"`）。正文照“设计稿 · 抽屉”逐项实现（字段顺序、键名小字 `.key` 灰色等宽、帮助文字逐字）；类型下拉 value `stdio`/`streamable_http`。
- `McpKeyValueRows`：一行一对（键、值两个等宽输入 + 删除图标按钮 `v-tip:top="删除"`），下方 `添加环境变量` / `添加请求头` 按钮（`justify-self: start`，不要撑满）；只读时没有删除与添加，空时写 `未设置`。
- 类型下拉切换 stdio/http 时，高级区跟着换（原型 `data-mcptype`）。
- “测试连接”：调用 `mcpTest(scope, ownerId, mcpWritePayload(null, 当前表单))`（测试用全量）；显示 `测试中…` → 成功 `✓ 已连接` 绿徽标 + `N 个工具 · x.x s` + `工具列表`/`收起工具` 链接（展开为等宽小标签列表）；失败 `✗ 连接失败` 红徽标 + 原文（等宽、可折行）。测试不写配置、不关抽屉。
- 保存：新增 `mcpCreate`、编辑 `mcpUpdate`（载荷 `mcpWritePayload(原行, 表单)`）；按钮 `保存中…`；失败时表单底部错误块：`没有保存：` + 服务端原文（409 同名、400 校验）。
- 状态框（编辑/查看时）：来源、状态（`mcpStatus`）、失败原因；（+）OAuth 授权状态文字（`需要授权` / `授权失败` / `已授权`）、官方标记、工具列表（可折叠）、“下个会话起不加载”、被覆盖时 `被{主代理/项目}覆盖`。
  **现状核实：前端没有任何 PKCE 调用**（`grep -rln pkce frontend/src` 为空），本计划不新做授权向导，报告里写明。
- 关闭：Esc、点遮罩、`取消`、右上 X；保存中不允许关闭。

**Verify**：`McpServerDrawer.test.ts`：只读时没有可编辑的 `input`/`select`（全部 `readonly`/`disabled`）、底部只有 `取消`；保存 409 时显示 `没有保存：` 错误块且抽屉不关；测试连接三态；`pnpm test -- src/components/mcp` 通过。

### Step 6：三处页面接上

当前主代理 id 一律用 `const { activePrimaryId } = usePrimaryAgents()`（`composables/usePrimaryAgents.ts`，`App.vue:357-365` 同样这样取），模板里写 `:agent-id="activePrimaryId"`。
- `McpTab.vue`：整体替换为 `<McpServersTable view="global" :agent-id="activePrimaryId" />`（标题行删除；设置页其它布局留给 076）。
- 新建 `views/McpView.vue`：`<McpServersTable view="agent" :agent-id="activePrimaryId" />`，外层滚动容器照 `ProjectsView.vue` 的外层写法。
  `router/index.ts` 加 `{ path: '/mcp', name: 'mcp', component: () => import('@/views/McpView.vue'), meta: { title: () => t('routes.mcpTitle') } }`；`App.vue` `navItems` 在技能后插 `{ to: '/mcp', label: L.mcp, icon: Plug, scope: 'agent' }`。
  **文案键已存在，直接用、不要重复添加**：`'nav.mcp'`（`locales/index.ts:183`，en `:1562`）、`'routes.mcpTitle'`（`:190`，en `:1569`）、`navLabels()` 里的 `mcp: t('nav.mcp')`（`:2828`）。
- `ProjectMcp.vue`：保留信任门（未信任时只显示信任提示与按钮）；已信任时显示 `<McpServersTable view="project" :project-id="projectId" :agent-id="activePrimaryId" />`；删掉旧的待同意列表与有效列表（已并入表格）与标题行。

**Verify**：`pnpm test` 全绿；`npx vue-tsc --noEmit -p tsconfig.json` exit 0（重复键会在这里报 TS1117）；`grep -n "path: '/mcp'" frontend/src/router/index.ts` 有一行。

### Step 7：e2e `frontend/e2e/mcp-pages.spec.ts`（新建）+ 回归

造配置：全局 `a`、`b`，主代理 `b`、`c`，项目 `d`（已同意）、`e` 与 `f`（待同意）。断言：
1. 设置 · MCP：全局两行（`a`、`b`），没有来源列；`b` 的状态是 `未运行 · 被覆盖`；`编辑 a` 打开抽屉、标题 `编辑 MCP 服务 · a`、底部 `取消`+`保存`；改请求超时保存后列表刷新。
2. 菜单栏出现 `MCP`；页面行顺序 `b(主代理) c(主代理) a(全局)`；全局行操作只有 `查看`，抽屉只读（没有可编辑的 input、底部只有 `取消`）、有 `在“设置 → MCP”修改` 链接。
3. 项目 MCP 标签：顺序 `e f(待确认) d(项目) b c(主代理) a(全局)`；`e` 行操作是 `允许`/`拒绝`；点 `e` 的 `允许` 后 `e` 状态变 `已允许`、**`f` 仍是 `待确认`**。
4. `添加 MCP 服务`（项目）→ 抽屉标题 `添加 MCP 服务 · 项目`；名称填已存在的 `d` 保存 → 抽屉不关、底部错误块含 `没有保存：`。
5. 测试连接：对一个本地不存在的 http 地址测试 → 出现 `✗ 连接失败` 与原文。
6. 编辑一个带 `env: { CODEGRAPH_HOME: … }` 的条目只改超时并保存 → 配置文件里 env 键仍是 `CODEGRAPH_HOME`。
7. 每页都有分页器；全局造 12 条后设置页显示 `共 12 条`、2 个页码。
全套回归：旧用例只允许因“旧 MCP 卡片/文字按钮/标题行”改断言，逐条列在报告里。

**Verify**：`FOREBRAIN_E2E_SPEC=e2e/mcp-pages.spec.ts scripts/acceptance/web_e2e.sh` 末行 `web e2e: PASS`；全套 `scripts/acceptance/web_e2e.sh` 末行 `web e2e: PASS`。

## Test plan

Step 1 的 Go 用例；`mcpRows.test.ts`（状态、类型、载荷差分）；`McpServerDrawer.test.ts`（只读时无可编辑控件、保存失败显示错误块、测试结果三态）；`McpServersTable.test.ts`（三种 view 的列与操作按钮）；Step 7 e2e。
截图 `$TMPDIR/074-*.png`：三处页面、查看抽屉、编辑（环境变量）、编辑（OAuth）、测试成功/失败、名称重复，各与原型对应状态并排对照。

## Done criteria

- [ ] `CGO_ENABLED=1 go test -tags fts5 ./pkg/gateway -count=1` ok；`go vet`、`gofmt` 无输出
- [ ] `pnpm test` 全绿、`vue-tsc` exit 0
- [ ] `mcp-pages.spec.ts` 与全套 `web_e2e.sh` → `web e2e: PASS`
- [ ] `grep -n 'PageTopRow' frontend/src/components/settings/McpTab.vue frontend/src/components/project/ProjectMcp.vue` 无输出
- [ ] 与开工前相比（开工时先 `git status --short > $TMPDIR/074-before.txt`），新增或修改的文件都在 Scope 内（`git status --short | diff $TMPDIR/074-before.txt -` 里出现的路径逐个核对）

## STOP conditions

- Drift check 失败；071 未完成。
- 真实响应的行字段与 `readMCPServerWrite`/`mcpServerRow` 不一致到无法映射（例如没有 `transport`）。
- 主代理层写接口要求的 `:id` 不是当前主代理 id（或切换主代理后写到了别的主代理）。
- 项目预览里拿不到待同意条目的配置内容，且没有它们表格就无法显示命令/类型——报告后再定是否只显示名称。
- 需要改写入语义（`mcpServerWrite` 的合并规则）才能让表单保存成功。
- owner 否决了 Step 1.4（同意只决定一条）——那就保留“一次决定全部”，并在待确认行的“允许/拒绝”旁用提示说明会一起决定其它待确认项，报告后再定。

## Maintenance notes

- 三处页面共用 `McpServersTable` / `McpServerDrawer`，差别只在 `view`；新增一层或改覆盖规则时同时改后端三个读视图与 `layers/own`。
- 表格状态列只放一个徽标，细节（失败原文、OAuth、`enabled: false`）一律放悬停提示和抽屉状态框——不要再往表格里加徽标。
- 写载荷只送变化字段，依赖后端“没送的字段不动、`[REDACTED]` 保留原值”的约定；改后端合并规则必须同步改 `mcpWritePayload`。
