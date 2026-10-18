# Plan 076：设置页——分组子导航、各标签无标题（表格标签：搜索 + 表格 + 分页 + 表下说明；表单标签：打平、标准单选组、底部保存）、运行状态平铺列表；最后删掉 `PageTopRow`

> **执行者须知**：按步骤顺序执行，每步跑验证命令并确认预期结果后再进下一步。触发
> "STOP conditions" 时停下报告，不要即兴发挥。**不要更新 `plans/README.md`**（评审方维护索引）。
> **禁止提交代码**（owner 手动提交）：不建分支、不建 worktree、不 commit、不 push、不 stash；改动留在当前工作区。
> 先跑 `scripts/fetch-libtokenizers.sh`；`go test` 报 pdfium 找不到时先 `make pdfium pdfium-fonts` 并照 Makefile 导出 `PKG_CONFIG_PATH`。
>
> **前置**：plan 071、073、074、075 已完成（`RadioField`、`SkillsTable` 新接口、`McpTab` 已换成 `McpServersTable`）。**本计划是这一组的最后一份**：最后一步删 `PageTopRow`，前面所有页面必须都已不用它。
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
- **Depends on**: plans/071、073、074、075
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

**子导航**（左侧竖排，带分组小标题，原型 `.subnav .grp`）：
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

**共享技能**：`SkillsTable`（scope `shared`）+ 工具栏（左 `搜索技能` + 来源 `全部来源/共享/内置`；右 下载所选、离线安装、在线安装、刷新）；分页；说明 `对所有主代理生效。内置技能只读。`

**配置文件**：工具栏左 路径 `~/.forebrain/forebrain.yaml`（等宽灰字，用实际路径）、右 `保存并生效`（主色）、`刷新`；下面编辑器（现有 `FileEditor`）；编辑器下方说明 `保存前按运行时的规则校验，通过后立即生效；密钥已脱敏，保持原样即保留原值。`（现有 `config.description`）。

**运行状态**：一张平铺的键值列表（原型 `kvList`，左列 110px 灰字），无外框无标题：
`状态`（`正常` 绿徽标 / `异常` 红徽标 + 原文）｜`版本`｜`监听地址`（等宽）｜`鉴权`｜`主代理`（`N 个`）｜`主目录`（等宽）｜`日志`（等宽）｜`状态库`（等宽）｜`技能`（+：`发现 N 个，启用 M 个`）。

## Current state（行号为 `4888d76`，已核实）

- `views/SettingsView.vue:1-34` 左侧子导航（无分组）+ 右侧标签内容；`:56-68` 标签数组（顺序 外观、审批默认、MCP、LSP、钩子、定时任务、记忆、配置文件、运行状态、共享技能）。
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

**In scope**：`pkg/gateway/api_extra.go`（`handleHooks`、`handleLSPSnapshot` 加分页；新增 `handleRuntimeInfo`）、`pkg/gateway/server.go`（只为拿到监听地址时需要的最小改动）及测试；
`frontend/src/views/SettingsView.vue`、`frontend/src/components/settings/*.vue`（除 `McpTab.vue`）、`frontend/src/components/common/SwitchRow.vue`（新建）+ 测试、`frontend/src/components/project/ProjectOverview.vue`（**只**把两个开关行换成 `SwitchRow`，不改别的）、`frontend/src/lib/api.ts`（钩子/LSP 分页版本、`runtimeInfo()`）、`frontend/src/locales/index.ts`、
`frontend/src/components/common/PageTopRow.vue` 与 `PageTopRow.test.ts`（删除）、`frontend/src/gallery/ComponentGallery.vue`（去掉 PageTopRow 示例）、`frontend/src/assets/main.css`（删 `.forebrain-page-top*`）、
`frontend/e2e/settings-tabs.spec.ts`（新建）及因旧设计断言失败必须更新的既有 e2e（只改断言）。

**Out of scope**：设置里各项的保存语义（审批默认、记忆开关、保留天数、配置文件校验）；LSP 的启用/安装实现；MCP 标签（074）。

## Steps

### Step 1：后端——钩子与 LSP 分页、运行状态接口

- `handleHooks` GET、`handleLSPSnapshot`：照 071 约定加服务端分页（不带 `page` 原样）。钩子 `q` 匹配事件、匹配、命令；LSP `q` 匹配 id 与语言，带 page 时返回 `servers` 一页 + `total`，`projectNotes` 等其它字段照旧在顶层。
- 新增 `GET /api/runtime`（`handleRuntimeInfo`，注册在 `AttachExtraRoutes` 里与 `/config` 同处）：返回
  `{"status": "ok", "version": <构建版本，用现有 banner 打印版本的同一来源>, "listen_addr": <网关实际监听地址>, "auth": <鉴权方式，与启动 banner 的 Auth 行同口径>, "agents": <主代理数>, "home": s.Home, "logs_dir": <日志目录>, "state_dir": <状态库目录>}`。
  每个值都从**现有**来源取（启动 banner `plans` 002 打印的同一批信息）；某个值拿不到就省略该键（前端显示 `—`），**不要**为了拿值改启动流程。
- 测试：钩子/LSP 各一条分页用例 + 一条不带 page 用例；`TestRuntimeInfoReportsHomeAndVersion`。

### Step 2：子导航分组

`SettingsView.vue`：标签数组改为三组（见目标结构）；子导航渲染分组小标题（原型 `.subnav .grp`：11.5px、灰、上下留白），按钮样式不变。

### Step 3：表单标签（外观、审批默认、定时任务、记忆）

- 全部去掉外框、`CardComponent`、标题行；表单直接铺在标签内容区，字段间距 12px（原型 `.flat { display: grid; gap: 12px }`）。
- 外观：两个 `RadioField`（主题色带 `swatch`；菜单栏）+ 语言下拉（调用与顶栏相同的 `setLocale`）。即时生效。
- 审批默认：`RadioField vertical`，选项 `description` = 现有预设说明；底部 `保存`。
- 定时任务（+）：照目标结构，按钮行在底部。
- 记忆：开关行列表（与 075 项目设置的开关行同一样式，抽成 `components/common/SwitchRow.vue` 两处共用）+ 帮助 + 底部 `保存` / `重置全部记忆`。

### Step 4：表格标签（LSP、钩子、共享技能）

照目标结构：删 `PageTopRow`；`ListToolbar`（左搜索必有）+ 表格 + `TablePager` + `ListNote`；数据走分页版本接口（`useServerList`）。
LSP 的操作按状态给图标按钮（逻辑沿用现有显示条件）；“详情”抽屉里的“复制日志路径”沿用现有 `copyLog`。现有错误/提示文案保留到悬停提示或抽屉里，不进表格。

### Step 5：配置文件、运行状态

- 配置文件：工具栏（左路径、右 `保存并生效`、`刷新`）+ 编辑器 + `ListNote`（`config.description`）。
- 运行状态：调 `forebrainApi.runtimeInfo()` + 现有技能统计，渲染 `kvList` 样式的平铺列表（用 `<dl class="forebrain-kv">`，在 `main.css` 按原型 `.kv { display: grid; grid-template-columns: 110px 1fr; gap: 6px 12px; font-size: 12.5px }`、`.kv dt { color: muted }` 定义）。
  `状态`：`/healthz` 200 → `正常`；否则 `异常` + 原文。

### Step 6：全站图标按钮的提示统一 + 工坊评测表

设计稿规则（owner 2026-10-17/18）：**所有只有图标的按钮**悬停（及键盘聚焦）都显示设计稿的紧凑提示条，**不再用浏览器原生 `title`**；表格操作栏一律图标按钮。前面各计划只改了它们自己的页面，这一步把剩下的扫干净：
1. `grep -rln ':title="t(\|title="' frontend/src/components frontend/src/views`（4888d76 时约 48 个文件）。逐个看：**只有图标、没有可见文字**的按钮/链接，把 `title` 换成 `v-tip`（方向：顶栏里 `bottom`、菜单栏里 `right`、其它默认 `top`），`aria-label` 保留；**有可见文字**的元素上的 `title`（截断全文提示等）换成 `v-tip:top.trunc`；其它（如 `<abbr>`、`iframe`）不动。
2. `components/workshop/EvalsTab.vue:49` 的文字按钮 `{{ t('workshop.view') }}` 改成 `RowAction`（`eye`，`查看`），所在列右对齐（原型评测表：`app-preview.html` 里搜 `data-fm="wseval:`，那一行的 `act('eye', '查看', …)`）。该表原型没有分页器——**保持没有**（原型即规格）。
3. Scope 追加：上面 grep 命中的 `.vue` 文件（只允许改 `title`→`v-tip` 这一类改动）、`EvalsTab.vue`。

**Verify**：`grep -rn 'title=' frontend/src/components frontend/src/views | grep -i 'icon-btn\|<button[^>]*aria-label' ` 人工复核剩余项都是“有可见文字”的合法用法，并在报告里列出保留的条目；`pnpm test` 全绿。

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
7. **全站检查**（一次性）：依次打开菜单栏所有页面、项目空间所有标签、设置所有标签，断言内容区都没有 `h2`、所有表格下方都有 `table-pager`（工坊评测表除外）、所有表格操作列只有 `forebrain-row-action` 按钮；随机悬停 5 个只有图标的按钮，都出现 `role=tooltip` 且元素上没有 `title` 属性。
全套回归：旧用例只允许因“标题行、卡片、大按钮单选、页面级保存位置”改断言，逐条列在报告里。

## Test plan

Step 1 的 Go 用例；`SwitchRow.test.ts`；各标签至少一条组件单测（无标题、结构正确）；Step 8 e2e。截图 `$TMPDIR/076-*.png`：10 个标签，与原型对应标签并排对照（LSP、定时任务两个原型没有的标签，对照同类标签的规则）。

## Done criteria

- [ ] Go：`./pkg/gateway` 全过；`go vet`、`gofmt` 无输出
- [ ] 前端：`pnpm test` 全绿、`vue-tsc` exit 0
- [ ] `grep -rn 'PageTopRow\|forebrain-page-top\|CardComponent' frontend/src/components/settings frontend/src/views/SettingsView.vue` 无输出；`PageTopRow.vue` 不存在
- [ ] `settings-tabs.spec.ts` 与全套 `web_e2e.sh` → `web e2e: PASS`
- [ ] `git status` 只出现 Scope 列出的文件

## STOP conditions

- Drift check 最后一条多出 Scope 以外的文件（前面的计划没做完）。
- 监听地址、鉴权方式在 `Server` 上拿不到且需要改启动流程才能拿到——省略这些键并在报告里写明，不要改启动流程；若连版本、主目录都拿不到 → STOP。
- 审批默认的保存接口要求的值与 `RadioField` 选项对不上。
- 删 `PageTopRow` 时发现还有页面在用。

## Maintenance notes

- 至此 web 所有页面与标签都没有内容标题；新增设置标签按“表格标签 / 表单标签”两种规则之一做，不要再加卡片或标题。
- `SwitchRow` 是设置与项目设置共用的开关行；新的开关项都用它。
- 运行状态接口只读、只汇总现有信息，不承担健康检查（健康检查仍是 `/healthz`）。
