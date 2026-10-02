# 计划 007：租户化应用外壳——最终一级菜单、设置页合并、主代理增删改查（ID 不可变）、子代理页

> **执行者须知**：逐步执行，每一步都运行"验证"命令并确认结果后再进入下一步。出现"STOP 条件"中的任何情况，立即停止并报告。
> 完成后更新 README 中本计划的状态行。**不要提交代码。**
>
> **漂移检查（先运行）**：
> `git diff --stat b92a1b1..HEAD -- frontend/src/App.vue frontend/src/router/index.ts frontend/src/views/SettingsView.vue frontend/src/components/ChatDrawer.vue pkg/gateway/agents_api.go pkg/gateway/api_extra.go pkg/config/primary_agent.go pkg/config/agents.go`
> 计划 001 改过 `App.vue`（登录分支）、003 改过 `SettingsView.vue`（外观卡片）与 `App.vue`（主题逻辑）、005 改过 `App.vue`（菜单栏/对话抽屉/折叠）。这些都是预期内的。

## 状态

- **优先级**：P1
- **工作量**：L
- **风险**：MED-HIGH（前端信息架构重组 + 新增主代理写 API；靠 Go 单测、前端单测与真机用例兜底）
- **依赖**：计划 003（外观卡片与主题变量）、计划 005（打平菜单、对话抽屉、折叠菜单栏）；决策 D7 已定（MCP、钩子移到设置）、D8 已定（共享技能库在设置，技能页归属计划 010）
- **类别**：direction
- **基线**：提交 `b92a1b1`，2026-09-29

## 为什么要做

owner 已确认全站预览（`app-preview.html`，菜单定义在其 373-377 行）。预览把应用重组为三层：**主代理 = 租户**（菜单页只放当前主代理的数据，切换只能用菜单栏顶部的下拉选择器）、**全局**（设置页）、**项目**（独立项目空间，计划 008）。现状是一批平铺路由：`/agents`、`/mcp`、`/hooks`、`/config` 各自为页，设置页混杂了权限记忆等本属于权限页的内容；主代理只能切换、不能增删改。本计划落地外壳：最终菜单清单、设置页合并、主代理的完整增删改查。

页面内容的归属：规则指令页=计划 009、技能页=计划 010、技能工坊=计划 011、记忆页=计划 012、项目空间=计划 008。本计划只建菜单入口里当时已存在的页面，并为尚未存在的页面留出菜单位置（见设计第 1 条）。

## 现状

- `frontend/src/router/index.ts`：14 条路由——`/`、`/agents`、`/permissions`、`/tools`、`/mcp`、`/projects`、`/projects/:id`、`/cron`、`/channels`、`/providers`、`/hooks`、`/config`、`/memories`、`/settings`。
- `frontend/src/views/AgentsView.vue`（131 行）：主代理列表 + 切换按钮（预览要求删除的"其他切换入口"）。`McpView.vue`、`HooksView.vue`、`ConfigView.vue` 是将被并入设置页的独立页。
- `frontend/src/views/SettingsView.vue`（644 行）：左栏两张信息卡（健康检查、技能概览），右栏表单卡；其中"权限记忆"区与 `PermissionsView.vue` 重复（README 既有缺陷表已把这条记在本计划名下）。计划 003 在左栏顶部加了"外观"卡片。
- 计划 005 已交付：打平的菜单栏（仍按现有 14 条路由排列）、`ChatDrawer.vue` 对话抽屉、菜单折叠与 `RailTooltip.vue`、主代理下拉（`data-act="tmenu"`）。005 明确把"菜单项增删"留给我计划。
- **既有 e2e 用例会访问本计划要删除的页面**（这是本计划唯一需要跨计划改动的地方，见设计第 2 条与范围）：
  - `frontend/e2e/visual.spec.ts`（计划 003 建立）遍历 13 个页面，其中包含 `/agents`、`/mcp`、`/hooks`、`/config`；
  - `frontend/e2e/layout.spec.ts`（计划 005 建立）有一个用例导航到 `/agents` 再返回。
  - `web_e2e.sh` 会运行 `frontend/e2e/` 下的全部 spec，所以这两份不更新，本计划的"`web_e2e.sh` PASS"完成标准无法达成。
- 主代理后端：
  - `pkg/config/primary_agent.go:15` `Summary{ID, WorkspaceRoot, PrivateSkillsRoot, SharedSkillsRoots, Active}`——**没有**显示名与说明字段；
  - `:36` `Resolver`（只读），`:67` `Active()`，`:177` `Switch()`，`:242` `summaryFor`（`:251-256` 填 `SharedSkillsRoots` 默认值）；
  - `pkg/config/agents.go:17` `AgentDefinition{Primary bool, LLMProviders, Channels}`，配置里主代理集合是 `agents.definitions` map；
  - `pkg/gateway/agents_api.go:48` `handlePrimaryAgents`（GET），`:62` `handlePrimaryAgentSwitch`（POST），路由注册在 `pkg/gateway/api_extra.go:63-64`；
  - `:137` `applyPrimaryAgent`——切换后重置网关的租户态缓存（`wsTreeCache` 等，注释明确"cannot drift"），`:183` `agentSwitchDeps`；
  - 没有创建/编辑/删除主代理的任何 API。
- 运行中的子代理数据源已存在：`GET /api/agents/roster`（`frontend/src/lib/api.ts:872`，`AgentRosterRow` 含 `sessionId`），`frontend/src/composables/useAgentRoster.ts` 提供加载与取消，`agentRosterViewTarget()` 生成跳转会话的路由；取消端点 `POST /api/agents/cancel-all`、`POST /api/agents/:agentId/cancel`（`api_extra.go:66-67`）、`POST /api/subagents/:id/cancel`（`:68`）。
- README 全局规则（90-115 行）全部适用：不提交代码、`pkg/gateway/dist` 不入库、真机验证、提示缓存命中率不变、双语、不留外部产品痕迹、修根因、死代码删除、包依赖图、禁粉色。

## 设计

### 1. 最终一级菜单（对齐预览 nav，`app-preview.html:373-377`）

顺序与图标（lucide）：对话 `message-square`（005 已建，点击弹对话抽屉）、项目 `folder-git-2` → `/projects`、规则指令 `scroll-text` → `/rules`、技能 `sparkles` → `/skills`、技能工坊 `hammer` → `/workshop`、子代理 `users` → `/subagents`、定时任务 `clock` → `/cron`、通道 `radio` → `/channels`、模型服务 `boxes` → `/providers`、工具 `wrench` → `/tools`、权限 `shield-check` → `/permissions`、记忆 `brain` → `/memories`；底部：设置（全局）、心跳（005 已建）、深浅切换（005 已建）。

本计划交付其中已存在或本计划新建的页面：对话（005）、项目（现有 `/projects`，008 重构）、子代理（本计划新建）、定时任务、通道、模型服务、工具、权限、记忆（现有视图，后续计划各自改造内容）。**规则指令、技能、技能工坊三个菜单项不在本计划添加**——它们的页面分别在 009、010、011 交付，由各自计划往菜单数组里加自己的一项（与 005"菜单项增删留给 007"、007"留给页面归属计划"同一原则）。菜单数据源改成一份带 `meta.scope`（`agent`/`global`/`project`）的数组，供各页顶部的作用域徽标（预览的 `scope(kind)` 样式：主代理/项目/全局）使用。

### 2. 设置页合并为标签页

`SettingsView.vue` 重构为左侧竖排标签（预览 `settingsTab: 'appearance'`）：**外观**（003 的外观卡片迁入，且必须是默认选中的标签）、**主代理**（本计划新建，见第 3 条）、**MCP**（`McpView.vue` 内容迁入）、**钩子**（`HooksView.vue` 内容迁入）、**记忆开关**（现设置页的三个记忆开关保留）、**配置文件**（`ConfigView.vue` 内容迁入）、**运行状态**（健康检查卡片迁入）、**技能（临时）**（现设置页的"技能概览"+"Skills 管理"卡原样搬进一个标签，含 `components/settings/SkillsInstalledList.vue`；本计划不动它的业务逻辑，计划 010 交付 `/skills` 页面与"共享技能"标签时才把这张卡和该组件删掉）。页头标"全局"作用域徽标。"审批默认"标签由计划 009 添加、"共享技能"标签由计划 010 添加——本计划把标签结构做成可扩展数组即可。

**删除**路由 `/agents`、`/mcp`、`/hooks`、`/config` 与对应视图文件（`AgentsView.vue`、`McpView.vue`、`HooksView.vue`、`ConfigView.vue`，连同只测它们的测试）。原设置页的"权限记忆"区整体删除（与权限页重复，README 既有缺陷表第 143 行）；权限记忆只在 `/permissions` 一处维护。

### 3. 主代理管理（设置 → 主代理标签页）

- **列表**：每个主代理一行/一卡：显示名、ID、工作区根、当前主代理标记（`Active`）、状态。行内只有"编辑""删除"，**没有切换按钮**——切换只能用菜单栏顶部下拉（005 已建）。
- **新建**：表单字段——ID（小写字母数字与连字符，唯一）、显示名、说明、工作区根（必须已存在的目录）。创建不改变当前主代理。
- **编辑**：只能改显示名与说明。**ID 不可变**（owner 2026-09-29 拍板）：不做迁移重命名租户键、工作区目录；编辑表单里 ID 只读展示。
- **删除**：拒绝删除当前主代理（`Active`）与 `Primary: true` 的默认主代理；确认对话框明示将删除该主代理的定义（`agents.definitions` 条目），工作区目录与历史数据保留在磁盘上，由 owner 手工清理——删除是配置层操作，不做文件系统破坏。
- 后端：
  - `config.Summary` 增加 `Name string`、`Description string`（json `name`/`description`）；`AgentDefinition` 增加 `DisplayName string \`yaml:"display_name,omitempty"\``、`Description string \`yaml:"description,omitempty"\``；`summaryFor`（`primary_agent.go:242`）填充。
  - 新端点（注册在 `api_extra.go:63-64` 旁，handler 放 `agents_api.go`）：
    - `POST /api/agents/primary` `{id, name, description, workspace_root}`：校验 ID 格式与唯一性、`workspace_root` 是已存在目录；写 `cfg.Agents.Definitions[id] = AgentDefinition{DisplayName, Description}`；`ensurePrimaryAgentDirs`（`primary_agent.go:319`）建目录；`s.saveAndReload(cfg)`（`api_extra.go:2836`）落盘并热加载。复用 `validatePrimaryAgents`（`pkg/config/load.go:620`）的既有校验。
    - `PUT /api/agents/primary/{id}` `{name, description}`：只改这两个字段，其余原样保留。
    - `DELETE /api/agents/primary/{id}`：拒绝 active 与 `Primary: true`；从 `agents.definitions` 删除后 `saveAndReload`。
  - `GET /api/agents/primary`（`handlePrimaryAgents`）响应随 `Summary` 自动带上 `name`/`description`。
  - 写路径全部走 `persistedConfig`（`api_extra.go:2820`）→ 改 → `saveAndReload`（`:2836`），与现有通道/模型服务写路径同一模式。

### 4. 切换主代理 = 重新划定前端一切范围

切换成功后（`POST /api/agents/primary/switch`），前端必须丢弃并重取所有租户维度数据：会话列表（对话抽屉）、项目列表、定时任务、通道、模型服务、工具、权限规则、记忆、子代理名册。做法：建 `frontend/src/composables/useTenantScope.ts`，暴露 `resetTenantScope()`；切换成功后调用，各页面 composable 在 `onTenantReset` 上重挂载/重取。后端缓存重置已存在（`applyPrimaryAgent`，`agents_api.go:137`），前端照用即可。

### 5. 子代理页（`/subagents`）

新建 `frontend/src/views/SubagentsView.vue`：`GET /api/agents/roster` 列出本主代理所有会话里正在运行的代理（表格：会话标题、代理名、启动时间、状态），行内"跳到所在会话"（复用 `agentRosterViewTarget`）、"停止"；页头"全部停止"（`POST /api/agents/cancel-all`）。空状态与 005 的工作台空规则一致（不显示卡片墙）。原 `AgentRosterPanel.vue` 在工作台内的用法不变。

## 需要的命令

| 用途 | 命令 | 成功标志 |
| --- | --- | --- |
| Go 静态检查 | `go vet ./...` | 退出码 0 |
| Go 测试 | `CGO_ENABLED=1 go test -tags fts5 ./pkg/gateway/... ./pkg/config/... -count=1` | 全部 `ok` |
| 前端单测 | `cd frontend && corepack pnpm test` | 全部通过 |
| 前端构建 | `cd frontend && corepack pnpm build --outDir "$TMPDIR/fb-webui" --emptyOutDir` | 退出码 0 |
| 真机验证 | `scripts/acceptance/web_e2e.sh` | 最后一行 `web e2e: PASS` |
| 缓存不变式 | `git diff --stat -- pkg/agent pkg/assembly pkg/llm pkg/turn/prompt* internal/agentrun` | 无输出 |

## 范围

**只允许修改**：
- `pkg/config/primary_agent.go`（Summary 字段、summaryFor）、`pkg/config/agents.go`（AgentDefinition 字段）、`pkg/config/load.go`（如主代理校验需要提示名）
- `pkg/gateway/agents_api.go`（CRUD handler）、`pkg/gateway/api_extra.go`（仅路由注册行）、`pkg/gateway/agents_api_test.go`（新建/扩展）
- `frontend/src/router/index.ts`、`frontend/src/App.vue`（菜单数组与 scope 徽标）、`frontend/src/locales/index.ts`
- `frontend/src/views/SettingsView.vue`（标签化，默认标签为"外观"）、`frontend/src/views/SubagentsView.vue`（新建）、`frontend/src/components/settings/*`（新建的标签内容组件）
- 删除 `frontend/src/views/AgentsView.vue`、`McpView.vue`、`HooksView.vue`、`ConfigView.vue` 及其测试 `frontend/src/views/McpView.test.ts`（内容先迁入设置标签）
- `frontend/e2e/visual.spec.ts`（计划 003 建立，本计划删掉其中 `/agents`、`/mcp`、`/hooks`、`/config` 四个页面、加入 `/subagents`）、`frontend/e2e/layout.spec.ts`（计划 005 建立，本计划把其中的 `/agents` 导航改为 `/subagents`）
- `frontend/src/composables/useTenantScope.ts`（新建）及其测试；`frontend/e2e/tenant-shell.spec.ts`（新建）

**不要碰**：
- `pkg/tui/**`（终端不动）；`pkg/gateway/dist/**`。
- 计划 008-014 的页面内容（规则指令、技能、工坊、记忆页、项目空间内部、计划编辑器、providers 表单）。
- `applyPrimaryAgent` 的缓存重置语义（已存在，不要重写）。

## 步骤

### 第 1 步：主代理后端 CRUD

实现设计第 3 条的后端部分，`agents_api_test.go` 覆盖：创建成功（含目录创建与配置落盘）、ID 重复被拒、workspace_root 不存在被拒、编辑只改 name/description（ID 不变、LLMProviders/Channels 原样）、删除 active 被拒、删除 `Primary: true` 被拒、删除成功后 GET 不再返回。

**验证**：`CGO_ENABLED=1 go test -tags fts5 ./pkg/gateway/... ./pkg/config/... -count=1` 全部 `ok`；`go vet ./...` 退出码 0。

### 第 2 步：设置页标签化、旧路由删除、既有 e2e 用例更新

实现设计第 2 条。迁移顺序：先把 `McpView`/`HooksView`/`ConfigView` 的模板与逻辑抽成 `components/settings/` 下的标签组件，确认设置页可用后再删路由与视图文件。外观卡片（003）迁入"外观"标签，**"外观"必须是默认选中的标签**（计划 003 的 `visual.spec.ts` 进入 `/settings` 后直接操作外观卡片，默认标签不是它就会连带弄坏 003 的用例）；删除设置页的权限记忆区。视图文件删除时连同 `frontend/src/views/McpView.test.ts` 一并处理（内容迁入新标签组件后，把该测试改为测新组件，或删除只测旧视图的部分）。

**同步更新更早计划建立的两份 e2e 用例**（它们会访问本计划删除的四个页面；不改就会被 `web_e2e.sh` 判失败，本计划的完成标准就达不成）：
- `frontend/e2e/visual.spec.ts`（计划 003）：页面清单里的 `/agents`、`/mcp`、`/hooks`、`/config` 删掉，改列 `/subagents`；其余 9 个页面与断言不变。
- `frontend/e2e/layout.spec.ts`（计划 005）：把"进入 `/agents` 再回到 `/`"改为"进入 `/subagents` 再回到 `/`"，其余断言不变。

**验证**：`grep -rn "path: '/agents'\|path: '/mcp'\|path: '/hooks'\|path: '/config'" frontend/src/router/index.ts` 无输出；`test ! -e frontend/src/views/AgentsView.vue` 等 4 条退出码 0；`grep -rn "McpView\|HooksView\|ConfigView\|AgentsView" frontend/src` 无输出（若新标签组件沿用了旧文件名，则按实际情况调整本条；旧视图的组件名不应残留）；`grep -rn "'/agents'\|\"/agents\"\|'/mcp'\|'/hooks'\|'/config'" frontend/src frontend/e2e` 无输出（路由字符串全仓无残留）；`cd frontend && corepack pnpm test` 全部通过。

### 第 3 步：主代理管理标签 UI

实现设计第 3 条的前端部分：列表、新建表单、编辑表单（ID 只读）、删除确认。所有文案进 `locales/index.ts` 中英两表。

**验证**：`cd frontend && corepack pnpm test` 全部通过；`grep -c "name\b" frontend/src/locales/index.ts` ≥ 新增键数（人工核对双语成对）。

### 第 4 步：最终菜单、scope 徽标、子代理页

实现设计第 1、5 条。菜单数组带 `scope` 元数据；子代理页接 roster。

**验证**：`cd frontend && corepack pnpm test` 全部通过；`grep -n "subagents" frontend/src/router/index.ts` 有输出。

### 第 5 步：切换主代理的前端范围重置

实现设计第 4 条 `useTenantScope.ts`。

**验证**：`cd frontend && corepack pnpm test` 全部通过。

### 第 6 步：真机验证

新建 `frontend/e2e/tenant-shell.spec.ts`：
1. 登录后菜单项依次为：对话、项目、子代理、定时任务、通道、模型服务、工具、权限、记忆、设置（009/010/011 落地前不含规则指令/技能/技能工坊）；截图 `shell-menu.png`。
2. `/agents`、`/mcp`、`/hooks`、`/config` 均返回前端路由兜底且菜单无对应项；`/settings` 显示标签栏，含外观/主代理/MCP/钩子/记忆开关/配置文件/运行状态。
3. 设置 → 主代理：新建主代理（`e2e-helper`，显示名"E2E 助手"），列表出现且非当前；编辑其显示名成功、表单里 ID 只读；删除当前主代理被拒（界面显示错误）。
4. 菜单栏下拉切换到 `e2e-helper` 再切回：对话抽屉会话列表重取（网络层面看到会话接口重新请求）。
5. `/subagents`：空状态可见；截图 `shell-subagents.png`。

**验证**：`scripts/acceptance/web_e2e.sh` → 最后一行 `web e2e: PASS`。

## 完成标准（全部满足）

- [x] 第 1-6 步验证全部通过
- [x] `web e2e: PASS`（31/31，tenant-shell 6 用例 + auth/visual/layout 已同步更新）
- [x] 四路由/四视图/旧测试删除，全仓无残留（`/agents`、`/mcp`、`/hooks`、`/config` 仅剩 API 路径字符串，与前端路由无关）
- [x] 权限记忆区已删（仅在 /permissions 维护）；设置页 8 标签默认外观；003 外观用例通过
- [x] 人工核对：唯一切换入口为菜单栏下拉；AgentsView 的切换按钮随视图删除
- [x] 均无输出
- [x] README 状态行已更新

## STOP 条件

- "现状"摘录与代码对不上（尤其 `Summary` 字段、`applyPrimaryAgent` 行为）。
- 创建/删除主代理需要改动 `pkg/config` 之外的运行时包（例如需要重置 `pkg/run` 层状态）——先停下来报告，owner 拍板范围。
- 发现删除 `agents.definitions` 条目会破坏状态库外键或迁移假设。
- 需要修改"范围"之外的文件。

## 维护说明

- 菜单数组是唯一的导航事实来源；009/010/011 各加一项时保持 `meta.scope` 约定。
- 主代理 ID 是租户键：状态库 `agent_id`、工作区目录名、技能状态路径都以它为键，永不可变；"编辑"只允许 name/description。
- 后续每个租户维度页面接入时都必须注册 `useTenantScope` 的重置回调，否则切换主代理会串数据。
