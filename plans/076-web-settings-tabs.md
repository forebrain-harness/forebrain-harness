# Plan 076：设置页——分组子导航、各标签无标题（表格标签：搜索 + 表格 + 分页 + 表下说明；表单标签：打平、标准单选组、底部保存）、运行状态平铺列表；最后删掉 `PageTopRow`

> **执行者须知**：按步骤顺序执行，每步跑验证命令并确认预期结果后再进下一步。触发
> "STOP conditions" 时停下报告，不要即兴发挥。**不要更新 `plans/README.md`**（评审方维护索引）。
> **禁止提交代码**（owner 手动提交）：不建分支、不建 worktree、不 commit、不 push、不 stash；改动留在当前工作区。
> 先跑 `scripts/fetch-libtokenizers.sh`；`go test` 报 pdfium 找不到时先 `make pdfium pdfium-fonts` 并照 Makefile 导出 `PKG_CONFIG_PATH`。
>
> **行号说明**：本计划的行号按 `4888d76`；071–075 落地后各文件会偏移，**一律用给出的代码片段 grep 定位**，行号只作参考。
> **前置**：plan 071、072、073、074、075 已完成（`RadioField`、`SkillsTable` 新接口、`McpTab` 已换成 `McpServersTable`）。**本计划是这一组的最后一份**：最后一步删 `PageTopRow`，前面所有页面必须都已不用它。
> **Drift check（先跑）**：以下每条都必须各有输出，否则 STOP：
> ```
> test -f frontend/src/components/common/RadioField.vue && grep -n 'McpServersTable' frontend/src/components/settings/McpTab.vue
> grep -n "{ key: 'shared-skills', label: t('settings.tabSharedSkills') }," frontend/src/views/SettingsView.vue
> grep -n '<PageTopRow :title="t(.approval.title.)"' frontend/src/components/settings/SettingsApprovalTab.vue
> grep -n "fetch('/healthz'" frontend/src/components/settings/RuntimeStatusTab.vue
> grep -rln '<PageTopRow' frontend/src | sort
> ```
> 最后一条的输出应**只剩**：`src/components/settings/{ConfigTab,HooksTab,LspTab,MemorySwitchesTab,SettingsApprovalTab}.vue` 与 `src/gallery/ComponentGallery.vue`（其余页面已由 073–075 去掉）；多出别的文件 → STOP（前面的计划没做完）。

## Status

- **Priority**: P1
- **Effort**: M
- **Risk**: LOW–MED（主要是模板重排；新增一个只读的运行状态接口）
- **Depends on**: plans/071、072、073、074、075（Step 8 的全站检查覆盖 072 做的顶栏/菜单栏提示）
- **Category**: direction（设计稿落地）
- **Planned at**: commit `4888d76` + 工作区，2026-10-18

## Why this matters

owner 2026-10-18：“设置页也按同样要求改”——与菜单栏页面、项目空间标签同一套：**没有标题**；表格标签上方左搜索、右按钮，表格下方分页器与说明；
**只有一张表单的标签，表单直接铺在标签页里**（不套框、不加标题），表单里的单选用**标准单选组**（主题色带色块，审批模式竖排带说明），保存按钮在表单底部；
配置文件把路径和按钮放编辑器上方；运行状态合成一张平铺列表。owner 裁决：所有列表服务端分页；原型没画的现有标签（**LSP**、**定时任务**）保留并按同一套规则放；原型需要的数据后端补。
做完后整个 web 不再有任何“页面标题行”，`PageTopRow` 组件删除。

## 设计稿（验收依据，实施须 1:1 还原）

- 原型 https://claude.ai/artifact/KKfPW8dXWScuRdTzonsk2F（本地 `docs/plan/WEB_REDESIGN_AND_GATEWAY_ACCESS/app-preview.html`），点菜单栏底部“设置”；状态按钮 **设置 · MCP**、**设置 · 钩子**、**设置 · 配置文件（代码）**。
- 原型代码：`pages.settings`（子导航分组与每个标签的内容）、`radioField`、`tabTools`、`tabNote`、`kvList`；CSS `.subnav-wrap`、`.subnav`、`.subnav .grp`、`.radios*`、`.radio*`、`.kv`。

### 目标结构（“+”= 原型没画、按 owner 裁决保留的现有功能）

**子导航**（左侧竖排，带分组小标题，原型 `.subnav .grp`：`font-size: 11px; padding: 10px 10px 4px; letter-spacing: 0.04em; color: var(--forebrain-muted-text)`）：
- `常规`：`外观`、`审批默认`
- `全局服务`：`MCP`、`LSP`（+）、`钩子`、`定时任务`（+）、`记忆`、`共享技能`
- `高级`：`配置文件`、`运行状态`
`?tab=<key>` 的键不变（`appearance/approval/mcp/lsp/hooks/cron/memory/shared-skills/config/runtime`）。

**外观**（表单，即时生效、无保存按钮）：
- `主题色`：`RadioField` 横排，选项带 14px 色块：`海军蓝（默认）`（`#1f4a9e`）、`松石青`（`#0e6e7e`）、`石墨黑`（`#1c1c1f`）；帮助 `只保存在当前浏览器。`
- `菜单栏`：`RadioField` 横排 `深色`/`浅色`；帮助 `内容区始终白底，深浅只作用于菜单栏。`
- `语言`：下拉 `中文`/`English`（宽 160px，与顶栏语言菜单是同一个设置）。

**审批默认**（表单）：`默认审批模式`：`RadioField` **竖排**，每项 `{中文名} · {英文名}` + 下面一行灰字说明（三档：`只读 · Read Only`、`默认 · Default`、`完全访问 · Full Access`，说明逐字沿用现有预设文案）；
帮助 `对所有主代理的新会话生效；单个会话可在输入框旁临时切换。写在 forebrain.yaml · approval_policy + sandbox_mode。`（键名等宽）；底部 `保存`（主色，未改动时禁用）。

**MCP**：074 已完成，不动。

**LSP（+）**：工具栏左 `搜索语言服务`、右 `刷新`；表格 `forebrain-table--fixed`：语言服务（等宽 id，下一行灰字语言列表——**这一列允许两行**）｜范围 84px（`全局`/`主代理`/`项目` 徽标，同 MCP 来源样式）｜状态 120px（单个徽标；`lastError`/问题放悬停提示）｜
程序 自适应（等宽灰：`binaryPath · version`，截断）｜操作 112px：按状态给图标按钮——`启用`（`power`）/`停用`（`power-off`）、`重启`（`rotate-cw`）、`安装`（`download`，需要安装时）、`详情`（`eye`，打开 `ForebrainDrawer` 只读：二进制、版本、问题、日志路径（带“复制”按钮）、项目写入、最近错误原文）；缺的放空位。
现有的“项目文件”提示（`projectNotes`）并入表下说明。分页；说明 = 现有 `lsp.description` + 项目文件提示。

**钩子**：工具栏左 `搜索事件或命令`、右 `新增钩子`（主色）、`刷新`（+）；表格：事件（等宽）｜匹配（`匹配全部工具` 用常规字体，其余等宽）｜命令（等宽，截断）｜操作 88px：`编辑`、`删除`（红）；分页；说明 `对所有主代理生效。`（+ 现有钩子页的其它提示合并进来）。

**定时任务（+）**（全局的执行记录保留设置，表单）：打平：`执行记录保留天数`（数字输入 128px，范围提示沿用现有）+ 帮助（现有 `cronSettings.retentionDescription`；未配置时追加默认值提示）；底部按钮行 `保存`（主色）、`恢复默认`。

**记忆**（表单）：开关行列表（`启用记忆功能`、`使用记忆`、`生成记忆`，标题 + 灰字说明 + 右侧开关；与项目设置里的开关行同样式，无外框）；
帮助 `对所有主代理生效；各主代理的记忆在菜单栏“记忆”，项目记忆在项目空间。`；底部按钮行 `保存`（主色）+ 现有的 `重置全部记忆`（红色描边，+）。

**共享技能**：073 执行时**已经**给 `SettingsSharedSkillsTab.vue` 接好了 `ListToolbar` + `SkillsTable` + `TablePager`（文件里搜 `<ListToolbar>`）；本计划只在分页器后补 `ListNote`：`对所有主代理生效。内置技能只读。`

**配置文件**：工具栏左 路径 `~/.forebrain/forebrain.yaml`（等宽灰字，用实际路径）、右 `保存并生效`（主色）、`刷新`；下面编辑器（现有 `FileEditor`）；编辑器下方说明 `保存前按运行时的规则校验，通过后立即生效；密钥已脱敏，保持原样即保留原值。`（现有 `config.description`）。

**运行状态**：一张平铺的键值列表（原型 `kvList`，左列 110px 灰字），无外框无标题（**版本**取前端常量 `__FOREBRAIN_VERSION__`（`App.vue` 已这样用），**监听地址**取浏览器当前访问的 `window.location.host`，其余来自 Step 1 的 `/api/runtime`）：
`状态`（`正常` 绿徽标 / `异常` 红徽标 + 原文）｜`版本`｜`监听地址`（等宽）｜`鉴权`｜`主代理`（`N 个`）｜`主目录`（等宽）｜`日志`（等宽）｜`状态库`（等宽）｜`技能`（+：`发现 N 个，启用 M 个`）。

## Current state（行号为 `4888d76`，已核实）

- `views/SettingsView.vue:1-34` 左侧子导航（无分组）+ 右侧标签内容；`:62-73` 标签数组（顺序 外观、审批默认、MCP、LSP、钩子、定时任务、记忆、配置文件、运行状态、共享技能）。
- `components/settings/AppearanceTab.vue:1-46` 一个带标题的框：主题色三个大按钮（带色块）、菜单栏两个按钮；没有语言。
- `components/settings/SettingsApprovalTab.vue:3` 标题行 + 预设大按钮（`ApprovalPresetPicker` 一类）。
- `components/settings/LspTab.vue:4` 标题行 + 卡片列表（`:30-90`：id、语言、范围、状态、二进制/问题/日志（复制）/项目写入、最近错误、启用/停用/重启/安装等文字按钮）。
- `components/settings/HooksTab.vue:3-12` 标题行（新增、刷新）+ `:19-` 表格（事件/匹配/命令/文字按钮）。
- `components/settings/CronSettingsTab.vue:9-52` 一张 `CardComponent`（标题“执行记录保留”、说明、天数输入、保存、恢复默认）。
- `components/settings/MemorySwitchesTab.vue:3-30` 标题行（保存在标题行右侧）+ 开关列表 + 重置按钮。
- `components/settings/SettingsSharedSkillsTab.vue` 用 `SkillsTable`（073 已改接口，本计划补齐工具栏/分页/说明）。
- `components/settings/ConfigTab.vue:3-8` 标题行 `forebrain.yaml`（刷新、应用）+ 编辑器。
- `components/settings/RuntimeStatusTab.vue:1-14` 两个框：健康检查（`fetch('/healthz')`）、技能统计；**没有**版本、监听地址、鉴权、主代理数、目录。后端**没有**运行状态接口（`/healthz` 只回 200）。
- `components/common/PageTopRow.vue` + `PageTopRow.test.ts`；`gallery/ComponentGallery.vue:52` 用它。
- 后端：`api_extra.go:5098` `handleHooks`（GET 全量）；`:6478` `handleLSPSnapshot`（`/api/v1/lsp`，全量快照）。

## Commands you will need

| 用途 | 命令 | 成功预期 |
|------|------|----------|
| gateway 包 | `CGO_ENABLED=1 go test -tags fts5 ./pkg/gateway -count=1` | ok |
| vet / fmt | `go vet ./pkg/... ./cmd/...` ；`gofmt -l cmd pkg` | 无输出 |
| 前端 | `cd frontend && pnpm test` ；`npx vue-tsc --noEmit -p tsconfig.json` | 全绿 / exit 0 |
| 单个 e2e | `FOREBRAIN_E2E_SPEC=e2e/settings-tabs.spec.ts scripts/acceptance/web_e2e.sh` | `web e2e: PASS` |
| 全套 e2e | `scripts/acceptance/web_e2e.sh` | `web e2e: PASS` |

## Scope

**In scope**：`pkg/gateway/api_extra.go`（`handleHooks`、`handleLSPSnapshot` 加分页；新增 `handleRuntimeInfo`）及测试（**不改** `serve_run.go`/`server.go`，不改启动流程）；`frontend/src/App.vue`（**只**把剩下的原生 `title` 换成 `v-tip`，见 Step 6）；
`frontend/src/views/SettingsView.vue`、`frontend/src/components/settings/*.vue`（除 `McpTab.vue`）、`frontend/src/components/common/SwitchRow.vue`（新建）+ 测试、`frontend/src/components/project/ProjectOverview.vue`（**只**把两个开关行换成 `SwitchRow`，不改别的）、`frontend/src/lib/api.ts`（钩子/LSP 分页版本、`runtimeInfo()`）、`frontend/src/locales/index.ts`、
`frontend/src/components/common/PageTopRow.vue` 与 `PageTopRow.test.ts`（删除）、`frontend/src/gallery/ComponentGallery.vue`（去掉 PageTopRow 示例）、`frontend/src/assets/main.css`（删 `.forebrain-page-top*`）、
`frontend/e2e/settings-tabs.spec.ts`（新建）及因旧设计断言失败必须更新的既有 e2e（只改断言）。

**Out of scope**：设置里各项的保存语义（审批默认、记忆开关、保留天数、配置文件校验）；LSP 的启用/安装实现；MCP 标签（074）。

## Steps

### Step 1：后端——钩子与 LSP 分页、运行状态接口

- **钩子**：`handleHooks` 的 GET 现在返回的是 `map[event][]HookMatcher`（`pkg/config/hook_schema.go` `type HooksSettings map[string][]HookMatcher`），前端 `HooksTab.vue` 自己摊平成行（行键 `${event}-${matcherIdx}-${commandIdx}`）。
  带 `page` 时：服务端按同样方式摊平，`q` 匹配事件、匹配、命令，返回 `rows: [{event, matcher, command, matcher_idx, command_idx}]` + `total/page/page_size`，原响应里的 `events`/`known_types` 等其它字段照旧在顶层；不带 `page` 原样。
  **编辑与删除仍是整表写回**（PUT 收整个 map）：前端在写之前先用不带 `page` 的 `hooks()` 取完整 map，在其上用 `event/matcher_idx/command_idx` 定位修改，再整体 PUT。
- **LSP**：`handleLSPSnapshot`（`/api/v1/lsp`）带 `page` 时 `q` 匹配 id 与语言，返回 `servers` 一页 + `total`，`projectNotes` 等其它字段照旧在顶层；不带 `page` 原样。前端取 `projectNotes` 的方式照 073 的 `ToolsView.vue`（在 `useServerList` 的 fetch 闭包里顺手存下顶层额外字段）。
- **新增 `GET /api/runtime`**（`handleRuntimeInfo`，注册在 `AttachExtraRoutes` 里与 `api.Get("/config", …)` 同处）：返回
  `{"status": "ok", "auth": gatewayAuthMode(s.liveCfg()), "agents": <主代理数>, "home": s.Home, "logs_dir": filepath.Join(s.Home, home.LogsDir), "state_dir": filepath.Join(s.Home, "state")}`
  （`gatewayAuthMode` 在 `controlplane.go`；`home.LogsDir` 在 `pkg/home/data.go`；主代理数照 `handlePrimaryAgents` 取列表的方式数）。**版本与监听地址由前端提供**，后端不返回，不改启动流程。`liveCfg()` 为 nil 时省略 `auth`。
- 测试：钩子/LSP 各一条分页用例 + 一条不带 page 用例；`TestRuntimeInfoReportsHomeAndDirs`。

**Verify**：`CGO_ENABLED=1 go test -tags fts5 ./pkg/gateway -run 'Hooks|LSP|RuntimeInfo' -count=1` ok（`-run` 用 `|`，不要写 `\|`）；`gofmt -l pkg` 无输出。

### Step 2：子导航分组

`SettingsView.vue`：标签数组改为三组（见目标结构）；子导航渲染分组小标题（样式见目标结构），按钮样式不变。

**Verify**：`SettingsView.test.ts`（若无则新建）：渲染出三个分组标题，按钮顺序与目标结构一致；`pnpm test` 全绿。

### Step 3：表单标签（外观、审批默认、定时任务、记忆）

- 全部去掉外框、`CardComponent`、标题行；表单直接铺在标签内容区，字段间距 12px（原型 `.flat { display: grid; gap: 12px }`）。
- 外观：两个 `RadioField`（主题色带 `swatch`；菜单栏）+ 语言下拉（调用与顶栏相同的 `setLocale`）。即时生效。
- 审批默认：`RadioField vertical`，选项 `description` = 现有预设说明；底部 `保存`。
- 定时任务（+）：照目标结构，按钮行在底部。
- 记忆：开关行列表（与 075 项目设置的开关行同一样式，**抽成 `components/common/SwitchRow.vue`**（props `title`、`hint`、`modelValue`、`disabled`；左标题 13px/500 + 下一行 12px 灰字，右 `SwitchComponent`），这里与 `ProjectOverview.vue` 两处共用）+ 帮助 + 底部 `保存` / `重置全部记忆`。

**Verify**：各标签组件单测：没有 `h2/h3`、没有 `CardComponent` 根类；外观改主题色后 `document.documentElement.dataset.brand` 变；`SwitchRow.test.ts`；`pnpm test` 全绿。

### Step 4：表格标签（LSP、钩子；共享技能补说明）

照目标结构：删 `PageTopRow`；`ListToolbar`（左搜索必有）+ 表格 + `TablePager` + `ListNote`；数据走分页版本接口（`useServerList`）；写法照 073 已落地的 `views/ProjectsView.vue`（搜索框高度用 `h-[34px]`，见 073 Step 7）。
LSP 的操作按状态给图标按钮（显示条件沿用现有 `LspTab.vue` 里各文字按钮的 `v-if`）；“详情”抽屉里的“复制日志路径”沿用现有 `copyLog`。现有错误/提示文案保留到悬停提示或抽屉里，不进表格。
钩子的编辑/删除按 Step 1 的“先取全量再整表写回”。共享技能只补 `ListNote`。

**Verify**：`grep -n 'PageTopRow' frontend/src/components/settings/{LspTab,HooksTab}.vue` 无输出；`pnpm test` 全绿。

### Step 5：配置文件、运行状态

- 配置文件：工具栏（左路径、右 `保存并生效`、`刷新`）+ 编辑器 + `ListNote`（`config.description`）。
- 运行状态：调 `forebrainApi.runtimeInfo()`（新方法，`GET /api/runtime`）+ 现有技能统计，渲染 `kvList` 样式的平铺列表（`<dl class="forebrain-kv">`，在 `main.css` 按原型 `.kv { display: grid; grid-template-columns: 110px 1fr; gap: 6px 12px; font-size: 12.5px }`、`.kv dt { color: var(--forebrain-muted-text) }` 定义）。
  `状态`：`/healthz` 200 → `正常`；否则 `异常` + 原文。`版本` = `__FOREBRAIN_VERSION__`；`监听地址` = `window.location.host`；接口缺的键显示 `—`。

**Verify**：`RuntimeStatusTab.test.ts`：mock `runtimeInfo` 后九行都在、缺键显示 `—`；`pnpm test` 全绿。

### Step 6：全站图标按钮的提示统一 + 工坊评测表

设计稿规则（owner 2026-10-17/18）：**所有只有图标的按钮**悬停（及键盘聚焦）都显示设计稿的紧凑提示条，**不再用浏览器原生 `title`**；表格操作栏一律图标按钮。前面各计划只改了它们自己的页面，这一步把剩下的扫干净。
1. 找候选：`grep -rnE '(:| )title="' frontend/src/App.vue frontend/src/components frontend/src/views --include='*.vue'`（2026-10-18 工作区约 61 个文件）。**只处理原生 HTML 元素**（`<button>`、`<a>`、`<span>`、`<div>`、`<RouterLink>` 等）上的 `title`；**组件的 `title` prop 不动**（`ForebrainModal`、`ConfirmDialog`、`FileEditDialog` 等的 `:title` 是标题，不是提示）。
2. 按用途替换：
   - 只有图标、没有可见文字的按钮/链接：`title` → `v-tip`（方向：顶栏里 `:bottom`、菜单栏里 `:right`、其它默认 `:top`），`aria-label` 保留；
   - 有可见文字、`title` 用来显示**被截断的全文**：→ `v-tip:top.trunc`；
   - 有可见文字、`title` 是**说明**（如禁用原因 `WorkshopTopbar.vue` 发布按钮的 `t('workshop.noDraftToPublish')`）：→ `v-tip:top`（**不带** `.trunc`，否则永远不显示），并同步修改断言这个 `title` 的单测（如 `WorkshopTopbar.test.ts` 改为断言悬停后的提示文字）；
   - 其它（`<abbr>`、`<iframe>` 等）不动。
   `App.vue` 里剩下的主代理删除图标按钮（搜 `:title="t('agents.deleteButton')"`）按第一类处理（`v-tip:right`）。
3. `components/workshop/EvalsTab.vue` 的文字按钮 `{{ t('workshop.view') }}` 改成 `RowAction`（`eye`，`查看`），所在列右对齐（原型评测表：`app-preview.html` 里搜 `data-fm="wseval:`，那一行的 `act('eye', '查看', …)`）。该表原型没有分页器——**保持没有**（原型即规格）。
4. Scope 追加：上面命中并实际改动的 `.vue` 文件（只允许 `title`→`v-tip` 这一类改动）及其单测、`EvalsTab.vue`。

**Verify**：重跑第 1 条 grep，剩余命中逐条核对都属于“组件 prop”或“其它”两类，并在报告里列出保留的条目；`pnpm test` 全绿。

### Step 7：删掉 `PageTopRow`

确认 `grep -rln '<PageTopRow' frontend/src` 只剩 `gallery/ComponentGallery.vue`；去掉 gallery 里的示例，删除 `PageTopRow.vue`、`PageTopRow.test.ts`、`main.css` 的 `.forebrain-page-top*` 规则。

**Verify**：`grep -rn 'PageTopRow\|forebrain-page-top' frontend/src` → 无输出；`pnpm test`、`vue-tsc` 通过。

### Step 8：e2e `frontend/e2e/settings-tabs.spec.ts`（新建）+ 全站回归

1. 子导航有 `常规`/`全局服务`/`高级` 三个分组标题，顺序与目标结构一致。
2. 10 个标签内容区都没有 `h2/h3` 与外框卡片（`.forebrain-card`/`CardComponent` 根类）。
3. 外观：主题色是 `role=radiogroup`、三个选项带色块；点 `松石青` 后根元素 `data-brand=teal`；菜单栏选 `浅色` 后外壳 `data-rail-theme=light`；语言改 `English` 后顶栏语言按钮文字同步。
4. 审批默认：竖排三项各带说明；改选后 `保存` 可点，保存后刷新仍是新值。
5. 钩子、LSP、共享技能：第一行最左 `input[type=search]`；有分页器；操作栏无文字按钮。
6. 运行状态：至少有 `状态`、`版本`、`主目录` 三行且值非空。
7. **全站检查**（一次性）：依次打开菜单栏页面（**不含对话页**——新对话问候语本来就是 `h2`）、项目空间所有标签、设置所有标签，断言：内容区都没有 `h2`；页面里**顶层**的表格（选择器 `.forebrain-table:not(.forebrain-table .forebrain-table)`，并排除弹窗与抽屉里的表、工坊评测表）下方都有 `table-pager`；所有表格操作列只有 `forebrain-row-action` 按钮；随机悬停 5 个只有图标的按钮，都出现 `role=tooltip` 且元素上没有 `title` 属性。
全套回归：旧用例只允许因“标题行、卡片、大按钮单选、页面级保存位置”改断言，逐条列在报告里。

**Verify**：`FOREBRAIN_E2E_SPEC=e2e/settings-tabs.spec.ts scripts/acceptance/web_e2e.sh` 末行 `web e2e: PASS`；全套 `scripts/acceptance/web_e2e.sh` 末行 `web e2e: PASS`。

## Test plan

Step 1 的 Go 用例；`SwitchRow.test.ts`；各标签至少一条组件单测（无标题、结构正确）；Step 8 e2e。截图 `$TMPDIR/076-*.png`：10 个标签，与原型对应标签并排对照（LSP、定时任务两个原型没有的标签，对照同类标签的规则）。

## Done criteria

- [ ] Go：`./pkg/gateway` 全过；`go vet`、`gofmt` 无输出
- [ ] 前端：`pnpm test` 全绿、`vue-tsc` exit 0
- [ ] `grep -rn 'PageTopRow\|forebrain-page-top\|CardComponent' frontend/src/components/settings frontend/src/views/SettingsView.vue` 无输出；`PageTopRow.vue` 不存在
- [ ] `settings-tabs.spec.ts` 与全套 `web_e2e.sh` → `web e2e: PASS`
- [ ] 与开工前相比（开工时先 `git status --short > $TMPDIR/076-before.txt`），新增或修改的文件都在 Scope 内（`git status --short | diff $TMPDIR/076-before.txt -` 里出现的路径逐个核对）

## STOP conditions

- Drift check 最后一条多出 Scope 以外的文件（前面的计划没做完）。
- `gatewayAuthMode`、`s.Home`、主代理列表任一在 `handleRuntimeInfo` 里拿不到（签名变了）——报告，不要改启动流程。
- 审批默认的保存接口要求的值与 `RadioField` 选项对不上。
- 删 `PageTopRow` 时发现还有页面在用。

## Maintenance notes

- 至此 web 所有页面与标签都没有内容标题；新增设置标签按“表格标签 / 表单标签”两种规则之一做，不要再加卡片或标题。
- `SwitchRow` 是设置与项目设置共用的开关行；新的开关项都用它。
- 运行状态接口只读、只汇总现有信息，不承担健康检查（健康检查仍是 `/healthz`）。
