# Plan 072：外壳与对话页——顶栏（深浅色切换、30px 按钮、标题截断、无模型名）、菜单栏（折叠按钮在底部）、首页即新对话、新对话居中且无标题/无工作台

> **执行者须知**：按步骤顺序执行，每步跑验证命令并确认预期结果后再进下一步。触发
> "STOP conditions" 时停下报告，不要即兴发挥。**不要更新 `plans/README.md`**（评审方维护索引）。
> **禁止提交代码**（owner 手动提交）：不建分支、不建 worktree、不 commit、不 push、不 stash；改动留在当前工作区。
>
> **前置**：plan 071 已完成（`frontend/src/directives/tip.ts`、`frontend/src/composables/useTooltip.ts` 存在，`RailTooltip.vue` 已删除）。
> **Drift check（先跑）**：以下每条都必须各有输出，否则 STOP：
> ```
> test -f frontend/src/directives/tip.ts && echo ok
> grep -n 'class="forebrain-rail-fold"' frontend/src/App.vue
> grep -n 'class="forebrain-rail-theme"' frontend/src/App.vue
> grep -n "h('span', { class: 'forebrain-topbar-model' }" frontend/src/views/ChatView.vue
> grep -n "void router.replace({ query: { ...route.query, session: lid } })" frontend/src/views/ChatView.vue
> grep -n 'data-testid="workshop-back"' frontend/src/components/workshop/WorkshopTopbar.vue
> ```

## Status

- **Priority**: P1
- **Effort**: M
- **Risk**: MED（改首页行为与对话页布局；e2e 里有几条用例编码了旧布局，要按新设计改断言）
- **Depends on**: plans/071-web-list-foundation.md
- **Category**: direction（设计稿落地）
- **Planned at**: commit `4888d76` + 工作区，2026-10-18

## Why this matters

owner 2026-10-18 在设计稿上定了外壳与对话页的一组细节：深浅色切换移到顶栏最右侧、菜单栏折叠按钮移到菜单栏底部“设置”右侧、
顶栏所有按钮同高 30px、语言按钮去掉下拉箭头、会话标题有最大长度（超出截断、悬停看全文）、顶栏不显示模型名、项目标签只写项目名、
对话抽屉标题只写“对话”、**登录后首页就是新对话页**、新对话页（还没有消息）里问候语+输入框+快捷入口在对话区**垂直居中**、
新对话页顶栏**不写“未命名对话”也不显示“工作台”**，发出第一条消息后才出现、标题即第一条消息的第一行（与引擎会话命名规则一致）。
现状与这些全部不同（见 Current state）。实施须 1:1 还原设计稿。

## 设计稿（验收依据）

- 原型 https://claude.ai/artifact/KKfPW8dXWScuRdTzonsk2F（本地 `docs/plan/WEB_REDESIGN_AND_GATEWAY_ACCESS/app-preview.html`）。
- 对应原型函数/样式：`rail()`（菜单栏，底部 `.r-foot` 里“设置”+ `.r-collapse`）、`topRight()`（顶栏右端：新建主代理、中文、深浅色切换 `.chip-btn.sq`）、
  `topbar()`（顶栏左侧：对话标题 `.top .t`、项目标签 `.scope.project`；`isFreshChat()` 时左侧为空、不画“工作台”）、`shownTitle()` / `sessionTitleOf()`、
  `drawer()`（对话抽屉标题只写“对话”）、`chat()`（`.nc`：问候语+输入框+快捷入口）、`wsTopbar()`（技能工坊顶栏）；
  CSS `.top`、`.top .t`、`.top > :not(.t):not(.crumb):not(.meta):not(.sp)`、`.top .meta`、`.top .crumb`、`.top .crumb > .t-name`、`.chip-btn`、`.chip-btn.sq`、`.top > .icon-btn`、`.top > .btn.sm`、`.scope`、`.scope.project`、`.nc`、`.r-foot`、`.r-collapse`、`.app[data-rc="1"] .r-foot*`。
- 原型状态按钮：**登录后首页（新对话）**、**新对话 · 选项目弹层**、**思考耗时 · 首 token**（普通对话）、**对话 · 超长标题**、**工坊 · 改进已有技能**；
  菜单栏底部的折叠按钮与顶栏的深浅色按钮可以直接点。
- 关键数值（原型原样）：
  ```css
  .top { display: flex; align-items: center; gap: 10px; height: 48px; padding: 0 20px; border-bottom: 1px solid var(--line); }
  .top .t { font-size: 14px; font-weight: 600; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; flex: 0 1 auto; min-width: 0; max-width: 360px; }
  .top > :not(.t):not(.crumb):not(.meta):not(.sp) { flex: none; }          /* 标题右侧的东西一律不缩 */
  .top .meta { flex: 0 1 auto; min-width: 0; overflow: hidden; text-overflow: ellipsis; font-size: 12px; color: var(--muted); white-space: nowrap; }
  .top .crumb { display: flex; align-items: center; gap: 6px; font-size: 14px; font-weight: 600; white-space: nowrap; min-width: 0; flex: 0 1 auto; }
  .top .crumb > * { flex: none; }  .top .crumb > .t-name { flex: 0 1 auto; min-width: 0; max-width: 360px; overflow: hidden; text-overflow: ellipsis; }
  .chip-btn { display: inline-flex; align-items: center; gap: 6px; height: 30px; box-sizing: border-box; border: 1px solid var(--line); border-radius: 6px; padding: 0 10px; font-size: 12px; color: var(--text-2); background: var(--page); }
  .chip-btn.sq { width: 30px; padding: 0; justify-content: center; }       /* 深浅色切换 */
  .top > .icon-btn { width: 30px; height: 30px; }  .top > .btn.sm { height: 30px; }   /* 顶栏里所有控件都是 30px（含边框） */
  .scope { display: inline-flex; align-items: center; gap: 5px; height: 22px; padding: 0 8px; border-radius: 5px; font-size: 11.5px; font-weight: 600; letter-spacing: 0.02em; }
  .scope.project { background: var(--text); color: #ffffff; }  .scope svg { width: 12px; height: 12px; }   /* 项目标签：文件夹图标 + 项目名 */
  .nc { flex: 1; min-height: 0; display: flex; flex-direction: column; align-items: center; justify-content: center; gap: 22px; padding: 24px; }
  .nc .composer { width: min(760px, 100%); }
  .r-foot { display: flex; align-items: center; gap: 4px; border-top: 1px solid var(--r-line); padding-top: 8px; }
  .r-collapse { margin-left: auto; width: 28px; height: 28px; display: grid; place-items: center; border: 0; border-radius: 6px; background: transparent; color: var(--r-fg2); }
  .r-collapse:hover { background: var(--r-hover); color: var(--r-fg); }
  .app[data-rc="1"] .r-foot { flex-direction: column; align-items: center; gap: 4px; }
  .app[data-rc="1"] .r-foot .r-collapse { margin-left: 0; width: 40px; height: 32px; }
  ```
- 文案（逐字）：深浅色按钮提示 `菜单栏切换为浅色` / `菜单栏切换为深色`（已有键 `theme.toLight` / `theme.toDark`）；
  折叠按钮提示 `折叠菜单栏` / `展开菜单栏`（已有键 `rail.collapse` / `rail.expand`）；项目标签悬停提示 `进入项目空间`；对话抽屉标题 `对话`。
- 提示条方向：顶栏里的按钮与标题 → **下方**（`v-tip:bottom`）；菜单栏里 → **右侧**（`v-tip:right`）。

## Current state（行号为 `4888d76`，已核实）

- `frontend/src/App.vue`
  - `:6-25` 菜单栏顶部：logo + 折叠按钮 `.forebrain-rail-fold`（`v-if="!railCollapsed"`）；`:26-40` 折叠后 logo 下方单独的展开按钮 `.forebrain-rail-expand`。
  - `:149-177` 菜单栏底部 `.forebrain-rail-foot`：“设置”链接 + 深浅色切换 `.forebrain-rail-theme`（`toggleRailTheme`、`Sun/Moon`、`themeToggleLabel`）。
  - `:192-243` 顶栏 `.forebrain-topbar`：页面标题 `<span class="forebrain-topbar-title">` 或视图贡献的 leading；spacer；视图贡献的 trailing；
    “新建主代理” `.forebrain-topbar-action`；语言 `.forebrain-language-trigger`（带 `ChevronDown class="forebrain-language-caret"`）+ 弹层。
  - `:401-409` `hoverTooltip/clearTooltip`（071 已改为调用 `showTip/hideTip`）；菜单栏各处 `@mouseenter="railCollapsed && hoverTooltip(...)"`。
  - `:180-188` `<ChatDrawer :agent-name="activePrimary?.id || ''" …>`。
- `frontend/src/assets/main.css`：`:539-545` `.forebrain-topbar { padding: 10px 16px; }`（实测高 51px，设计稿 48px）；`:547-551` `.forebrain-topbar-title { font-size: 13px; color: var(--forebrain-text-2); }`（无截断、无最大宽度）；
  `:555-560` `.forebrain-topbar-model`；`:563-575` `.chat-project-tag`（深色胶囊、无图标）；`:754-790` 顶栏三个按钮的共用样式与 `.forebrain-language-caret`；
  `:963+` `.forebrain-rail-fold/.forebrain-rail-expand`、`.forebrain-rail-theme` 与折叠态规则。
- `frontend/src/views/ChatView.vue`
  - `:14-23` 新对话页的问候语：放在对话区里 `flex h-full flex-col items-center justify-end`（**底部对齐**），输入框在页面底部原位；`:498` 快捷入口 `.starters` 在输入框区里。
  - `:690-725` `setTopbar`：leading = 标题 span（`truncate text-[13px] font-semibold`，`data-testid="chat-session-title"`）+ 项目标签（`t('chat.projectTag', { name })` = `项目 · {name}`，RouterLink 到项目概览）+ **模型名** `forebrain-topbar-model`；
    trailing = 心跳（有会话时）+ 工作台按钮（**总是显示**）。
  - `:668` `sessionTitle = sessionHeaderTitle(sessionInfo, t('nav.chat'))`——没有会话时显示“对话”。
  - `:757-758` `isFreshChat = !sessionId && !messages.length && !isStreaming && !activeAgentView`。
  - `:1984-1990` 根路径不带 `session` 且不带 `new` 时，**自动跳回上次的会话**（`lastSessionId`）——与“首页就是新对话页”相反。
- `frontend/src/components/ChatDrawer.vue:15,21` 标题与 aria-label 用 `t('chatDrawer.title', { agent })` = `对话 · 主代理 {agent}`。
- `frontend/src/components/workshop/WorkshopTopbar.vue`：`:3-12` 返回箭头按钮 `data-testid="workshop-back"`；`:13-19` 面包屑（叶子 `truncate`，无最大宽度）；
  `:27-36` 项目胶囊 `t('workshop.projectChip', { name })`；`:37` **模型名** `data-testid="workshop-model"`；`:41-52` 新会话图标按钮 28px（`h-7 w-7`）；`:53+` 工作台按钮用 `forebrain-btn`（不是 30px 胶囊）。
- 引擎会话命名规则（`pkg/state/session_store.go:2136-2156` `generateSessionTitle`）：去首尾空白 → 取第一行 → 超过 50 个字符（rune）截断加 `...`；空串 → `New session`。
- 受影响的 e2e：`frontend/e2e/tenant-shell.spec.ts`、`layout.spec.ts`、`new-chat-project.spec.ts`、`skill-workshop.spec.ts`、`workbench.spec.ts`，以及单测 `src/views/ChatView.test.ts`。

## Commands you will need

| 用途 | 命令 | 成功预期 |
|------|------|----------|
| 前端单测 | `cd frontend && pnpm test` | 全绿 |
| 前端类型 | `cd frontend && npx vue-tsc --noEmit -p tsconfig.json` | exit 0 |
| 单个 e2e | `FOREBRAIN_E2E_SPEC=e2e/shell-chrome.spec.ts scripts/acceptance/web_e2e.sh` | 末行 `web e2e: PASS` |
| 浏览器全套 | `scripts/acceptance/web_e2e.sh` | 末行 `web e2e: PASS` |

## Scope

**In scope**：`frontend/src/App.vue`、`frontend/src/assets/main.css`、`frontend/src/views/ChatView.vue`（+ `ChatView.test.ts`）、`frontend/src/components/ChatDrawer.vue`、
`frontend/src/components/workshop/WorkshopTopbar.vue`、`frontend/src/views/WorkshopView.vue`（仅当它给 WorkshopTopbar 传 `model` 时删掉该传参）、
`frontend/src/lib/sessionTitle.ts`（新建）+ `sessionTitle.test.ts`、`frontend/src/locales/index.ts`、
`frontend/e2e/shell-chrome.spec.ts`（新建），以及因旧设计断言失败而必须更新的既有 e2e 用例（只改断言，见 Step 9）。

**Out of scope**：项目空间的顶栏面包屑（075）；任何列表页（073–076）；会话命名的后端规则（不改 Go）；`useLastSession` 的其它用途（新对话预选项目、MCP 页取会话）。

## Steps

### Step 1：菜单栏——折叠/展开按钮移到底部“设置”右侧

- 删 `App.vue:14-24` 的 `.forebrain-rail-fold` 与 `:26-40` 的 `.forebrain-rail-expand`。
- 在 `.forebrain-rail-foot` 里“设置”链接**后面**放一个按钮（替换原深浅色按钮的位置）：
  `<button type="button" class="forebrain-rail-collapse" data-testid="rail-collapse" :aria-label="railCollapsed ? t('rail.expand') : t('rail.collapse')" :aria-expanded="!railCollapsed" v-tip:right="railCollapsed ? t('rail.expand') : t('rail.collapse')" @click="railCollapsed ? expandRail() : collapseRail()">`，
  图标 `PanelLeftOpen`（折叠态）/ `PanelLeftClose`（展开态），尺寸同原型 `.r-collapse`（展开态 28×28、`margin-left: auto`；折叠态 40×32、在“设置”下面）。
- 菜单栏其它 `@mouseenter/@focus=hoverTooltip…` 换成 `v-tip:right="railCollapsed ? label : ''"`（空串时指令不显示）；删除 `hoverTooltip/clearTooltip`。
- `main.css`：删 `.forebrain-rail-fold/.forebrain-rail-expand` 与 `.forebrain-rail-theme` 相关规则（含折叠态的），新增 `.forebrain-rail-collapse` 两态规则（变量用菜单栏的 `--forebrain-rail-*` 同名变量——照 `.forebrain-rail-theme` 原来用的那几个）。

**Verify**：`grep -n 'forebrain-rail-fold\|forebrain-rail-expand\|forebrain-rail-theme' frontend/src` → 无输出；`pnpm test` 全绿。

### Step 2：顶栏——深浅色切换放最右、语言按钮去箭头、所有控件 30px、标题截断

- 深浅色切换：从菜单栏删掉后，在语言菜单**后面**加 `<button type="button" class="forebrain-chip-btn forebrain-chip-btn--sq" data-testid="theme-toggle" :aria-label="themeToggleLabel" v-tip:bottom="themeToggleLabel" @click="toggleRailTheme">`，图标深色菜单栏时 `Sun`、浅色时 `Moon`。
- 语言按钮：删掉 `ChevronDown class="forebrain-language-caret"` 与对应 CSS；按钮本身保留弹层行为。
- 三个按钮统一用 `.forebrain-chip-btn` 的规格（30px、`box-sizing: border-box`、6px 圆角、`0 10px`），`.forebrain-chip-btn--sq` = 30×30、无内边距、居中。
- `.forebrain-topbar`：`height: 48px; padding: 0 20px; box-sizing: border-box;`（设计稿 `.top`）；顶栏直接子元素中除标题/面包屑/meta/spacer 外一律 `flex: none`。
- `.forebrain-topbar-title`：`font-size: 14px; font-weight: 600; color: var(--forebrain-text); white-space: nowrap; overflow: hidden; text-overflow: ellipsis; flex: 0 1 auto; min-width: 0; max-width: 360px;`，
  并在 App.vue 的标题 span 上加 `v-tip:bottom.trunc="pageTitle"`。

**Verify**：e2e（Step 8）里量到三个按钮高度都是 30；`grep -n 'forebrain-language-caret' frontend/src` → 无输出。

### Step 3：会话标题规则移植到前端（`src/lib/sessionTitle.ts`）

```ts
/** The engine's session-title rule (pkg/state generateSessionTitle): trimmed, first line, at most 50 characters then "...". */
export function sessionTitleFromContent(content: string): string
```
空串返回 `''`（前端用它做“临时标题”，空时不显示，**不要**返回 `New session`）。按 rune 截断：用 `Array.from(line)`。
`sessionTitle.test.ts` 覆盖：多行取第一行、首尾空白、49/50/51 个中文字符、emoji 不被截半。

### Step 4：对话页顶栏

改 `ChatView.vue:690-725` 的 `setTopbar`：
- leading：
  - `isFreshChat` 为真 → 返回 `[]`（**不写任何标题**）。
  - 否则标题 span：`class="forebrain-topbar-title"`、`data-testid="chat-session-title"`、`v-tip:bottom.trunc`（在 `h()` 里用 `withDirectives(h('span', …), [[tipDirective, sessionTitle.value, 'bottom', { trunc: true }]])`）。
    标题取值：`sessionInfo.title`；**若会话已建立但服务端标题还没回来**（`sessionInfo` 为空或其 title 等于会话 id），用 `sessionTitleFromContent(第一条用户消息文本)` 做临时标题；都没有时用 `t('nav.chat')`。
  - 项目标签：文字只写 `project.name`（删除 `chat.projectTag` 的用法与两种语言的键），前面加 `FolderGit2` 图标（12px），样式改成原型 `.scope.project`（见设计稿数值；改 `main.css` 的 `.chat-project-tag`），`v-tip:bottom` = `进入项目空间`（新键 `chat.openProjectSpace`，en `Open project space`），仍链接到项目空间。
  - **删掉模型名**（`forebrain-topbar-model` 的 `h('span', …)` 与 `main.css:555-560` 的类）。
- trailing：心跳不变（已按 `sessionId` 判断）；工作台按钮只在 `!isFreshChat` 时显示，样式 `forebrain-chip-btn`（已是）。

### Step 5：首页即新对话；新对话关闭工作台

- 删 `ChatView.vue:1984-1990`（根路径没有 `session` 时跳回 `lastSessionId` 的分支）。`new=1` 的处理保持（现在等价于不带参数）。
  `defaultTarget()`（新对话预选上次会话所在项目）**保留**。
- 进入新对话（`route.query.session` 变为空）时调用 `workbench.close()`；工作台面板的渲染条件加 `&& !isFreshChat.value`（防止从别处带着打开状态进来）。
- 登录页 `LoginView.vue` 不改：没有 `redirect` 时它跳 `/`，现在就是新对话页。

**Verify**：`ChatView.test.ts` 新增用例：①根路径无参数时不调用 `router.replace` 去上次会话；②`isFreshChat` 时 topbar leading 为空、trailing 不含 `workbench-toggle`。`pnpm test` 全绿。

### Step 6：新对话页布局——问候语 + 输入框 + 快捷入口在对话区垂直居中

把新对话页改成原型 `.nc` 的结构：`isFreshChat` 时，对话列容器加类 `chat-col--fresh`：`display: flex; flex-direction: column; justify-content: center; align-items: center; gap: 22px; padding: 24px;`；
问候语块从“占满剩余高度、底部对齐”改为 `flex: none`；输入框块宽 `min(760px, 100%)`、`flex: none`；快捷入口仍在输入框下方。
非新对话时布局**完全不变**（消息流在上、输入框在底部）。更新 `ChatView.vue:14-17` 的注释（不再“底部对齐”）。
**项目胶囊的弹层**（`ProjectPopover`）在居中后仍须完整可见：它向上展开，若在 1320×800 下顶部被裁切，改为在上方空间不足时向下展开（在弹层组件里按 `getBoundingClientRect` 判断），不要改回底部对齐。

**Verify**：Step 8 的 e2e 量 1320×800 下：问候语顶到对话区顶 与 快捷入口底到对话区底 的距离差 ≤ 8px；点项目胶囊后弹层的 `getBoundingClientRect()` 完全在视口内。

### Step 7：对话抽屉标题、技能工坊顶栏

- `ChatDrawer.vue`：标题与 aria-label 改 `t('chatDrawer.title')`；`'chatDrawer.title'` 的值改为 `对话` / `Chats`（删 `{agent}`）；删 `agentName` prop，`App.vue` 不再传。
- `WorkshopTopbar.vue`：
  - 删返回箭头按钮（`:3-12`），点面包屑“技能”返回（已有）。
  - 面包屑叶子加 `class="forebrain-topbar-name"`（= 原型 `.t-name`：`flex: 0 1 auto; min-width: 0; max-width: 360px; overflow: hidden; text-overflow: ellipsis;`，在 main.css 定义）+ `v-tip:bottom.trunc="label"`。
  - 删模型名（`:37`，连同 `model` prop；`WorkshopView.vue` 不再传）。
  - 项目胶囊：文字只写项目名，样式与对话页项目标签一致（`.chat-project-tag` 同一个类），`v-tip:bottom` = `进入项目空间`；删 `workshop.projectChip` 键。
  - 新会话图标按钮改 `forebrain-chip-btn forebrain-chip-btn--sq`（30×30），`v-tip:bottom="t('workshop.newSession')"`，删原生 `title`。
  - 工作台按钮改 `forebrain-chip-btn`（30px，`aria-pressed`）；“发布 vN”按钮高度 30px（若当前不是，加类 `h-[30px]`）。

### Step 8：新增 e2e `frontend/e2e/shell-chrome.spec.ts`

照 `e2e/tenant-shell.spec.ts` 的写法（登录、fake provider），用例：
1. 打开 `/`：出现问候语 `chat-greeting`，顶栏左侧没有 `chat-session-title`，没有 `workbench-toggle`；问候语与快捷入口上下留白差 ≤ 8px。
2. 顶栏右端依次是 `new-agent`、语言按钮、`theme-toggle`，三者 `boundingBox().height === 30`；点 `theme-toggle` 后外壳 `data-rail-theme` 在 dark/light 间切换，悬停时出现提示条（`role=tooltip`）且在按钮下方。
3. 菜单栏底部 `rail-collapse` 在“设置”右侧；点击后菜单栏宽 64、按钮在“设置”下方；再点恢复 264。
4. 在新对话里发一条多行消息（第一行 60 个字符）：消息发出后顶栏出现标题，等于第一行前 50 个字符 + `...`；出现 `workbench-toggle`，且工作台面板是收起的。
5. 用 API 造一个标题 80 字的会话并打开：标题元素宽度 ≤ 360、`scrollWidth > clientWidth`；顶栏所有按钮都在视口内、互不重叠；悬停标题出现全文提示。
6. 打开技能工坊：没有 `workshop-back`、没有 `workshop-model`；面包屑“技能”可返回。

### Step 9：跑全套 e2e，按新设计更新旧断言

`scripts/acceptance/web_e2e.sh`。失败的旧用例**只允许**因为下列旧设计而改断言（每改一处在报告里列出文件:行与原因）：
菜单栏折叠按钮位置、深浅色按钮位置、语言箭头、顶栏模型名、项目标签文字 `项目 · `、抽屉标题 `对话 · 主代理`、工坊返回箭头、根路径自动回到上次会话、新对话问候语底部对齐。
其它原因的失败 → STOP。

## Test plan

Step 3、5 的单测；Step 8 的 e2e；Step 9 全套回归。另附 4 张截图（`$TMPDIR/072-*.png`）：新对话首页、普通对话顶栏、超长标题顶栏、折叠菜单栏，并与原型对应状态并排对照。

## Done criteria

- [ ] `pnpm test` 全绿、`npx vue-tsc --noEmit -p tsconfig.json` exit 0
- [ ] `FOREBRAIN_E2E_SPEC=e2e/shell-chrome.spec.ts scripts/acceptance/web_e2e.sh` → `web e2e: PASS`；全套 `web_e2e.sh` → `web e2e: PASS`
- [ ] `grep -rn 'forebrain-topbar-model\|workshop-model\|forebrain-rail-theme\|forebrain-rail-fold\|chat.projectTag\|workshop.projectChip' frontend/src` 无输出
- [ ] `git status` 只出现 Scope 列出的文件

## STOP conditions

- Drift check 失败；071 未完成。
- 删掉“回到上次会话”后，有别的功能（不是首页）依赖这次跳转而坏掉（例如某个 e2e 断言“刷新后仍在原会话”——带 `?session=` 的刷新不受影响，若坏了说明理解错了）。
- 居中布局后项目弹层在 1320×800 下向上、向下都放不下。
- 发送第一条消息后服务端标题长时间不回来且临时标题也取不到第一条消息文本。

## Maintenance notes

- 顶栏的控件一律 30px；新加按钮用 `forebrain-chip-btn`（方形图标用 `--sq`），提示一律 `v-tip:bottom`。
- 顶栏标题/面包屑名的最大宽度是 360px，右侧元素都 `flex: none`；新增顶栏元素若需要可缩短，必须显式给 `min-width: 0` 和截断。
- 前端的临时标题规则必须与 `pkg/state.generateSessionTitle` 一致；改了一边要改另一边（`sessionTitle.test.ts` 与 Go 的单测用同一组样例）。
