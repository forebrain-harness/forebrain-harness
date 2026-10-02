# 计划 005：对话页重排——对话抽屉取代会话栏，菜单栏打平，工作台可收起且默认收起，心跳入口移到菜单栏，空的"运行中的代理"卡片不显示

> **执行者须知**：逐步执行，每一步都运行"验证"命令并确认结果后再进入下一步。出现"STOP 条件"中的任何情况，立即停止并报告。
> 完成后更新 README 中本计划的状态行。**不要提交代码。**
>
> **漂移检查（先运行）**：
> `git diff --stat b92a1b1..HEAD -- frontend/src/components/ChatSidebar.vue frontend/src/composables/useChatSessions.ts frontend/src/components/chat/SessionHeartbeat.vue frontend/src/composables/useAgentRoster.ts pkg/event/run_events.go pkg/tui/reducer.go pkg/tui/run.go pkg/gateway/api_extra.go`
> 计划 001、003、004 改过 `App.vue`、`ChatView.vue`、`main.css`、`locales/index.ts`，001 改过 `api_extra.go`（登录路由），那是预期的；先通读这几个文件的当前版本再动手。

## 状态

- **优先级**：P1
- **工作量**：L
- **风险**：MED（会话切换从组件内部事件改为由地址栏驱动；有真机用例覆盖）
- **依赖**：计划 003（菜单栏 `--rail-*` 变量）、计划 004（菜单栏品牌区）
- **类别**：direction
- **基线**：提交 `b92a1b1`，2026-09-29

## 为什么要做

owner 原话：
- "对话的layout必须重新设计，现在分了四栏，改成三栏布局，把第2栏（新聊天）删掉，放进左侧菜单栏，只保留左侧菜单栏、对话窗口、工作台"
- "心跳不要放进工作台，心跳不是常用功能，入口放在左侧菜单栏即可"
- "live agents为空时不要在工作台显示live agents卡片"
- "工作台必须可以收起可以打开，默认收起"；"默认情况下，只有左侧菜单栏和内容区"
- "左侧菜单栏改一下：恢复对话菜单项，点击对话菜单，向右弹出对话抽屉，把新聊天、搜索会话和会话列表等功能和操作入口都移到对话抽屉里，管控和工作区里的子菜单项都打平改成一级菜单，删掉管控和工作区菜单项"
- "切换入口只保留左侧菜单栏上方的下拉选择器，删掉其他切换按钮和操作入口"
- "左侧菜单栏必须可以折叠，只显示菜单图标，默认打开"
- "左侧菜单栏折叠后，鼠标移到菜单图标上方必须显示菜单项名称，方便用户知道这个是什么菜单"
- "对话窗口消息区的已工作 1 分 12 秒后面要加上完成时间，与tui保持一致"
- "☑3/3的checkbox图标再大一些……现在的图标太小了"；"web可以用iconfont"；"tui不改，只改web"

现在对话页是四栏：全局菜单栏、会话栏（新聊天 + 搜索 + 会话列表）、对话窗口、工作台（心跳、运行中的代理、工作区文件）。
会话栏与菜单栏功能上是同一件事（选择去哪里），分两栏既占宽度又割裂。本计划把会话栏并入菜单栏，心跳作为低频功能收进菜单栏底部；
工作台变成可收起的第三栏，**默认收起**——打开页面时只有菜单栏和内容区，需要时再展开工作台。工作区文件的目录树由计划 006 实现。

布局以 owner 确认后的全站预览 `docs/plan/WEB_REDESIGN_AND_GATEWAY_ACCESS/app-preview.html` 为准（对话页、工作台开合、菜单栏分组与底部入口）。

## 现状

- `frontend/src/App.vue`（计划 001/003/004 之后）：菜单栏 `<aside class="forebrain-rail">` 依次是品牌区、主代理（租户）卡片、4 个导航分组
  （`work`：对话；`control`：代理、项目、定时任务、通道、权限；`workspace`：工具、MCP、模型服务、钩子、记忆；`settings`（底部）：配置文件、设置）；
  右侧 `forebrain-work` 是顶栏（页面标题、语言、深浅切换）+ `<router-view>`，视图以 `` :key="`${route.fullPath}::${activePrimaryId}`" `` 重建。
- `frontend/src/views/ChatView.vue`（1848 行）：
  - `:4-6` 第一栏是 `<ChatSidebar :sessions :loading :active-id :error @new-chat @select>`；
  - `:572-605` 最右是工作台 `<aside class="hidden min-h-0 w-[320px] ... lg:flex">`：标题 `chat.workbench`、`<SessionHeartbeat :session-id="sessionId" />`、
    `<AgentRosterPanel :title="t('agents.liveTitle')" :records="rosterRecords" ...>`（空时也显示，内含"没有运行中的代理"）、工作区文件列表；
  - `:975` `const { sessions, loading: sessionsLoading, error: sessionsError, fetchSessions } = useChatSessions()`；
  - `:1478-1493` `handleNewChat`：`chatSessionCreate()` 失败时进入 `catch {}`，**静默**执行 `resetForNewChat()`——用户看不到任何错误；
  - `:1495-1504` `handleSelectSession` / `viewRoster`：`viewRoster` 先 `switchToSession` 再 `router.replace(agentRosterViewTarget(row))`；
  - `:1725-1730` 流式结束时 `fetchSessions()`（标题可能已被自动命名）；
  - `:1743-1758` `onMounted`：`fetchSessions()`，然后按 `route.query.session` 或 `lastSessionId` 打开会话。
- `frontend/src/components/ChatSidebar.vue`（97 行）：新聊天按钮、搜索框（前端按标题过滤）、会话列表、加载失败提示、底部一个"设置"链接。
- `frontend/src/composables/useChatSessions.ts`（43 行）：**每次调用都新建一组** `ref`，不是共享状态。
- `frontend/src/composables/useAgentRoster.ts:5-9`：`agentRosterViewTarget` 返回 `{ path: '/', query: { session, live: '1' } }`；全仓没有任何代码读取 `live` 参数。
- `frontend/src/components/chat/SessionHeartbeat.vue`（108 行）：心跳表单（分钟数、提示词、启用/更新/暂停/恢复/清除），`props: { sessionId }`，调用 `forebrainApi.heartbeat` / `saveHeartbeat` / 清除接口。
- `frontend/src/components/AgentRosterPanel.vue`：`ChatView` 与 `AgentsView` 共用；在"代理"页上空状态要照常显示。
- `frontend/src/locales/index.ts` 中文表的问题：`:111` `'agents.liveTitle': 'Live agents'`、`:113-114` `'{count} 个 agents'` / `'1 个 agent'`、`:444` `'chat.workspaceFiles': 'Workspace 文件'`（组件里还加了 `uppercase` 变成 "WORKSPACE 文件"）。

## 设计

### 1. 栏位

- 默认只有两栏：**菜单栏**（`App.vue`）与**内容区**（对话页即对话窗口：`ChatView` 的 `<main>`，最大宽度 880px 居中，保持现状）。
- **工作台**是可收起的第三栏，默认收起。删除 `ChatSidebar.vue`。
- 工作台开合状态放在新文件 `frontend/src/composables/useWorkbench.ts` 的模块级 `ref`（`open`、`toggle()`、`close()`），初始值 `false`：
  在同一个标签页里跨页面保留（去"代理"页再回来仍是打开的），**不写入 localStorage**——每次重新加载页面都从收起开始，保证"默认收起"。
- 开关按钮在对话页顶栏右侧、语言切换之前：图标 `PanelRight`，文字"工作台"，`aria-pressed` 反映开合，`data-testid="workbench-toggle"`；
  有运行中的代理时按钮上显示一个品牌色数字徽标（运行数），这样工作台收起时也能看到有代理在跑。
- 展开后：
  - 视口宽度 ≥ 1024px：工作台作为第三栏出现在内容区右侧，宽 300px，内容区相应变窄；
  - 视口宽度 < 1024px：工作台以抽屉形式从右侧覆盖在内容区上（`position: fixed`，顶部对齐顶栏下沿，宽 `min(300px, 100vw - 64px)`，
    左侧 1px `--forebrain-divider` 描边、`--forebrain-shadow-pop` 阴影），按 `Escape` 或点击抽屉外部关闭，菜单栏与内容区宽度不变。
- 工作台顶部一行：标题"工作台"与关闭按钮（`X` 图标，`aria-label`"收起工作台"）。

### 2. 菜单栏结构（自上而下，以 `app-preview.html` 为准）

1. 品牌区（计划 004）。
2. **主代理下拉选择器**：切换当前主代理的**唯一入口**（`aria-haspopup="listbox"`）。展开后列出全部主代理（名称 + 工作区路径），当前项前有圆点；选中即切换。
   下拉列表属于菜单栏的一部分，配色用菜单栏变量：背景 `--rail-pop-bg`（实色，深色菜单栏下为深底、浅色下为白底）、文字 `--rail-fg`、路径 `--rail-fg-2`、描边 `--rail-line`、悬停与当前项 `--rail-hover`、阴影 `--forebrain-shadow-pop`。
   它渲染在菜单栏元素之外，依赖计划 003 把 `--rail-*` 定义在外壳根元素上；若实施时发现变量取不到（背景透明），按 STOP 条件处理，不要在下拉组件里另写一份颜色。
   其他页面上的"切换"按钮一律删除（"代理"页的切换按钮由计划 007 随页面重组删除）。
3. **一级菜单（打平，无分组）**：`对话` 在最上面，其后是各主代理维度页面。删除原 `work`、`control`、`workspace` 三个分组与分组标题。
   具体菜单项清单与每个页面的内容归属由计划 007 按"主代理 / 全局 / 项目"三层重新划分（例如 MCP、钩子移入设置，技能成为一级菜单）；
   本计划只把现有路由打平排列，计划 007 再增删菜单项。
4. **`对话`菜单项**：点击不跳转，而是在菜单栏右侧**向右弹出对话抽屉**（新组件 `frontend/src/components/ChatDrawer.vue`，`data-testid="chat-drawer"`）；
   抽屉打开时 `对话` 项显示为展开态（行尾 `ChevronRight`），再次点击或点抽屉外、按 `Escape`、点抽屉右上角关闭按钮都会收起。抽屉内容自上而下：
   - 标题行"对话 · 主代理 <名称>"与关闭按钮；
   - "新聊天"主按钮（`data-testid="drawer-new-chat"`）；
   - 搜索框（前端按标题过滤）；
   - 小标题"会话"与会话列表：当前会话用 `--forebrain-brand-soft` 底、`--forebrain-brand-1` 字；当前会话若有启用中的心跳，行尾显示 13px `Activity` 图标（`data-testid="drawer-heartbeat-indicator"`）；
   - 底部一行说明"项目里的会话在各自的项目空间中"（项目会话不在抽屉里列出，见第 3 条）；
   - 空、加载中、加载失败沿用 `sidebar.empty`、`sidebar.loading`、`sidebar.loadFailed`（文案键改名为 `chatDrawer.*`），失败时显示服务端原文。
   选中会话或点"新聊天"后抽屉自动收起。抽屉是浮层：左缘贴菜单栏右缘，宽 300px，白底、右侧 1px `--forebrain-divider` 描边、`--forebrain-shadow-pop` 阴影，覆盖在内容区之上，不改变内容区宽度。
   对话页处于打开状态时 `对话` 项高亮（与其他一级菜单的选中样式相同）。
5. **底部**一行：`心跳`（按钮）、`设置`（链接；"设置"与"配置文件"合并为这一个入口，页面合并由计划 007 完成），最右是菜单栏深浅切换图标按钮（从顶栏移到这里，调用计划 003 的 `toggleRailTheme()`）。

6. **折叠**：品牌区右侧一个图标按钮（`PanelLeftClose`，`aria-label`"折叠菜单栏"，`aria-expanded="true"`，`data-testid="rail-collapse"`）把菜单栏折叠成 64px 宽、只显示图标；
   折叠后品牌区下方换成展开按钮（`PanelLeftOpen`，`aria-label`"展开菜单栏"，`aria-expanded="false"`）。
   - **默认展开**。开合状态放在 `frontend/src/composables/useRailCollapse.ts` 的模块级 `ref`，同一标签页内跨页面保留，**不写 localStorage**，每次重新加载都从展开开始（与工作台"默认收起"同样的处理）。
   - 折叠后：字标、菜单文字、`对话` 行尾箭头隐藏，菜单项居中只显示图标，每个图标按钮有 `aria-label`；
   - **名称提示**（折叠时必须有）：鼠标移到任一图标上，或用键盘聚焦到它，**立即**（不等浏览器原生 `title` 的延迟）在图标右侧 10px、垂直居中处显示该菜单项的名称；
     离开或失焦即隐藏。实现为一个自绘的提示元素（新组件 `frontend/src/components/RailTooltip.vue`，`role="tooltip"`，深色 `#101828` 实色底、白字、6px 圆角、左侧小三角，`pointer-events: none`），
     通过 `aria-describedby` 关联到当前图标；渲染在菜单栏元素之外，以免被菜单栏的 `overflow: hidden` 裁掉。折叠状态下**不再**给这些按钮设置 `title`，避免出现两个提示。
     覆盖的按钮：全部一级菜单、主代理首字母方块（"当前主代理：<名称>"）、展开按钮、底部的心跳、设置、深浅切换。
     主代理选择器缩成 40×40 的首字母方块（`title` 与 `aria-label` 为"当前主代理：<名称>"），点开的下拉列表显示在菜单栏右侧；底部三项竖排只显示图标。
   - 对话抽屉、心跳浮层、主代理下拉都从**当前**菜单栏右缘弹出（展开时 264px，折叠时 64px）。这三个浮层渲染在菜单栏元素之外（菜单栏自身 `overflow: hidden`），用菜单栏宽度计算位置。
   - 宽度变化有 160ms 过渡，`prefers-reduced-motion` 下关闭过渡。

窄屏（`max-width: 900px`）：菜单栏始终是折叠形态，隐藏折叠/展开按钮；对话抽屉照常从菜单栏右缘弹出（宽 `min(300px, 100vw - 64px)`）。

### 3. 会话由地址栏驱动

- `useChatSessions.ts` 改为模块级单例（写法仿照 `usePrimaryAgents.ts`）：`sessions`、`loading`、`error`、`fetchSessions()`、`createSession(): Promise<string>`（调用 `forebrainApi.chatSessionCreate()`，成功后 `fetchSessions()` 并返回新 id；失败抛错）。
  `sessions` 只含**不属于任何项目**的会话（owner 决策：项目会话只在项目空间列出）。如果现有 `GET /api/chat/sessions` 返回的记录里混有项目会话，按 STOP 条件停下报告，由计划 007 在接口层按 `project_id` 区分，而不是在前端过滤。
  主代理切换（`usePrimaryAgents` 的 `refreshToken` 变化）时清空并重新拉取——会话属于租户。
- 打开某个会话 = `router.push({ path: '/', query: { session: id } })`；"新聊天" = `const id = await createSession()` 后同样 `push`。创建失败时在对话抽屉顶部显示错误原文（`getErrorMessage`），**不做任何静默重置**。
- `App.vue` 视图的 `key` 改为 `` `${route.path}::${activePrimaryId}` ``：只改查询参数不重建 `ChatView`。
- `ChatView.vue`：
  - 删除 `ChatSidebar` 及 `handleNewChat`、`handleSelectSession`；
  - `watch(() => route.query.session, (v) => { const sid = String(v ?? '').trim(); if (sid && sid !== sessionId.value) switchToSession(sid) }, { immediate: true })`；
  - `watch(sessionId, (sid) => { if (sid && route.query.session !== sid) void router.replace({ query: { ...route.query, session: sid } }) })`，保证地址栏始终反映当前会话；
  - `onMounted` 里：没有 `route.query.session` 且有 `lastSessionId` 时 `router.replace({ query: { session: lastSessionId } })`，由上面的 watch 打开；
  - 流式结束时调用共享的 `fetchSessions()`（菜单栏标题随之更新）；
  - `viewRoster` 只做 `router.replace(target)`（watch 会打开会话），不再手动 `switchToSession`。
- `agentRosterViewTarget` 去掉无人读取的 `live: '1'`，同步更新 `useAgentRoster.test.ts`。
- 对话抽屉里"当前会话"的判定：`route.path === '/' && route.query.session === row.id`。
- 打开的是项目会话时，对话页顶栏在标题旁显示黑底"项目 · <项目名>"标签，点击进入该项目空间（项目空间由计划 007 实现；在此之前标签只显示不跳转）。

### 4. 心跳入口

- 新组件 `frontend/src/components/RailHeartbeat.vue`：菜单栏底部的"心跳"按钮 + 向上弹出的浮层（`role="dialog"`，`aria-label` 为心跳标题）。
  浮层是内容层，白底（`--forebrain-surface`）、`--forebrain-divider` 描边、`--forebrain-shadow-pop` 阴影，内部直接复用 `SessionHeartbeat.vue` 表单（去掉它外层的卡片边框样式，由浮层提供）。
- 作用对象是当前会话（`route.query.session`）；没有打开会话时按钮禁用，`title` 为 `heartbeat.needsSession`。
- 点击浮层外部或按 `Escape` 关闭；打开时焦点移入第一个输入框，关闭后焦点回到按钮。
- 心跳状态需要被会话列表的指示图标读取：新建 `frontend/src/composables/useSessionHeartbeat.ts`（模块级单例，持有当前会话的 `HeartbeatRecord | null`，提供 `load(sid)`、`save(...)`、`clear(sid)`），
  `SessionHeartbeat.vue` 改为使用它，而不是组件内部各自的 `ref`；`ChatDrawer` 读取同一份状态决定是否显示指示图标。
- 从 `ChatView` 工作台中删除 `<SessionHeartbeat>`。

### 5. 工作台

- 新组件 `frontend/src/components/chat/ChatWorkbench.vue`，把 `ChatView.vue:572-605` 的工作台整体搬进来（`data-testid="chat-workbench"`），props：`records`、`loading`、`error`（运行中的代理）、`workspaceTree`、`workspaceTreeLoading`，事件：`view`、`cancel`、`cancel-all`、`insert-ref`、`close`。
  （计划 006 会把工作区文件部分换成懒加载目录树组件，届时 `workspaceTree`、`workspaceTreeLoading` 两个 props 由组件自己取数代替。）
- 只在 `useWorkbench().open` 为真时渲染（`v-if`），收起时不占任何宽度。
- 运行中的代理列表（`loadRoster`）照常在对话页加载，用于顶栏按钮的徽标，与工作台是否展开无关。
- "运行中的代理"卡片 `v-if="records.length > 0 || error"`：**没有代理时整张卡片不显示**（加载中也不显示）；加载失败时显示，以免错误被藏起来。
- 工作区文件小标题去掉 `uppercase` 与字距。
- 中文文案修正：`agents.liveTitle` → `运行中的代理`；`agents.count` → `{count} 个代理`；`agents.countOne` → `1 个代理`；`chat.workspaceFiles` → `工作区文件`。

### 6. 顶栏

- 保留页面标题与语言切换；深浅切换按钮移走（见第 2 条第 5 项）；对话页顶栏新增工作台开关（见第 1 条）。
- 在对话页，标题显示当前会话标题（取自共享会话列表；无会话时显示 `nav.chat`）。

### 6b. 每轮结束行与终端一致

终端的结束行（`pkg/tui/reducer.go:1213-1223` `formatWorkedStatusTitle`，由 `pkg/tui/render.go:5388-5398` 画成整宽分隔线）是：

```
─ Worked for 1m 12s · ☑3/5 · <进行中的最短任务标题> · 15:04 ───────────
```
依次为：时长（`formatElapsedDuration`：`12s` / `1m 12s` / `1h 02m 03s`）、任务清单进度（仅当本轮有任务清单时，见 `pkg/tui/run.go:3549-3563` `formatWorkingCounters`：`☑完成数/总数`，有进行中的任务时再接最短的那个标题）、完成时刻的本地 `HH:MM`（`workedCompletionTime`）。

网页端现状（`frontend/src/composables/useChatStream.ts:1222-1238` `formatWorkedDurationLabel`）：有 `runFinishedAt` 时已经会追加 `· HH:MM`，但**缺少任务清单进度**；分隔线用了渐变且文字大写加宽字距（`ChatView.vue:209-217`）；中文表 `chat.workedFor` 仍是英文。按"终端语义为准、共享实现、不在网页端另写一套"的规则修正：

- **共享引擎**：把"任务清单进度"这一事实的计算从 `pkg/tui` 下沉到 `pkg/event`：新增 `func PlanProgressOf(items []PlanUpdateItem, fallback string) PlanProgress`（`Done`、`Total`、`Active`；`Active` 规则原样搬迁 `pkg/tui/reducer.go:1239` 起的 `shortestActiveTaskTitle`）。
  终端的 `Tracker` 改为调用它（行为不变，原有 `pkg/tui` 测试是第一道回归门）。
- **事件与历史**：运行结束事件的载荷（`pkg/event/run_events.go:104` 附近含 `ElapsedMS` 的结构）新增 `plan_done`、`plan_total`、`plan_active`，由引擎在运行结束时用 `PlanProgressOf` 基于本轮最后一次任务清单更新填入；
  历史消息接口（`pkg/gateway/api_extra.go:976` 附近含 `RunFinishedAt` 的记录）同样返回这三个字段，用同一个函数从已保存的任务清单计算。网页端只拿这些事实拼文案，不自己计算。
- **网页文案**：`formatWorkedDurationLabel` 改为返回结构化的几段而不是一整个字符串：`{ label: t('chat.workedFor', { duration }), plan?: { done, total, active }, time: 'HH:MM' }`（`time` 由 `runFinishedAt` 按浏览器本地时区取时分），时长格式与终端完全一致。
  模板按段渲染，段之间用 ` · ` 分隔；任务清单进度**不用文字字符 ☑**，而是 lucide 的 `SquareCheck` 图标（项目现有图标库，不另引入图标字体），尺寸 `1em × 1em`（与 `done/total` 数字同字号，随字号缩放）、`stroke-width: 2.25`（小尺寸下保持清晰）、颜色 `--forebrain-success`、右侧留 `0.2em`；
  **垂直对齐**：图标 `display: inline-block; vertical-align: -0.14em`，让图标中心落在数字的视觉中心（数字字形位于基线到大写字母高度之间，中心约在基线上方 0.36em）。不要用 `vertical-align: middle` 或 flex 居中——它们按小写字母高度或整行高度居中，图标会偏低（owner 在预览里指出过这个问题）；
  图标 `aria-hidden="true"`，进度段整体带 `aria-label`（中文"任务清单完成 {done}/{total}"，英文 "Checklist {done}/{total} done"）。有 `planActive` 时其后再接 ` · {active}`。
  **终端不改**：`pkg/tui` 里的 `☑` 字符保持原样（owner 决定），本条只动网页端。
  中文表 `chat.workedFor` 改为 `已工作 {duration}`，英文保持 `Worked for {duration}`。
- **样式**：左侧 12px 短线 + 文字 + 右侧填满剩余宽度的线，线为 1px 实色 `--forebrain-divider-strong`，文字 `--forebrain-muted-text`、等宽数字，去掉 `uppercase` 与字距。

### 7. 清理

- 删除 `frontend/src/components/ChatSidebar.vue` 与只被它使用的文案键 `sidebar.settings`（中英两表）。
- 删除 `navGroups` 中的 `work` 组；`navLabels().chat` 若只剩顶栏标题一个用途则保留。

## 需要的命令

| 用途 | 命令 | 成功标志 |
| --- | --- | --- |
| Go 测试 | `CGO_ENABLED=1 go test -tags fts5 ./pkg/event/... ./pkg/tui/... ./pkg/gateway/... -count=1` | 全部 `ok` |
| 前端单测 | `cd frontend && corepack pnpm test` | 全部通过 |
| 前端构建 | `cd frontend && corepack pnpm build --outDir "$TMPDIR/fb-webui" --emptyOutDir` | 退出码 0 |
| 真机验证 | `scripts/acceptance/web_e2e.sh` | 最后一行 `web e2e: PASS` |

## 范围

**只允许修改**：
- `pkg/event/plan_progress.go`（新建：`PlanProgressOf`）、`pkg/event/plan_progress_test.go`（新建）、`pkg/event/run_events.go`（`TurnCompletedPayload` 加 `plan_done`/`plan_total`/`plan_active` 三字段，见设计 6b）
- `pkg/tui/reducer.go`、`pkg/tui/run.go`（清单进度的既有实现改为转调 `pkg/event` 共享实现，行为不变）及 `pkg/tui` 对应测试
- `pkg/gateway/api_extra.go`（仅历史消息记录补 `plan_done`/`plan_total`/`plan_active`，见设计 6b）及其测试文件
- `frontend/src/App.vue`、`frontend/src/assets/main.css`（菜单栏样式）、`frontend/src/locales/index.ts`
- `frontend/src/views/ChatView.vue`
- `frontend/src/components/ChatDrawer.vue`、`frontend/src/components/RailHeartbeat.vue`、`frontend/src/components/RailTooltip.vue`、`frontend/src/components/chat/ChatWorkbench.vue`、`frontend/src/composables/useWorkbench.ts`、`frontend/src/composables/useRailCollapse.ts`（均新建）及其 `.test.ts`
- `frontend/src/components/chat/SessionHeartbeat.vue`、`frontend/src/composables/useSessionHeartbeat.ts`（新建）
- `frontend/src/composables/useChatSessions.ts`、`frontend/src/composables/useAgentRoster.ts`、`frontend/src/composables/useAgentRoster.test.ts`
- 删除 `frontend/src/components/ChatSidebar.vue`
- `frontend/e2e/layout.spec.ts`（新建）

**不要碰**：
- `AgentRosterPanel.vue` 的空状态：它在"代理"页照常显示空状态，隐藏规则只在工作台生效。
- `useChatStream.ts` 的会话加载逻辑（`switchToSession` / `resetForNewChat` 的内部实现）。
- 除设计 6b 明确列出的三处共享引擎改动（`pkg/event`、`pkg/tui` 清单进度实现、历史消息接口补字段）之外的任何后端接口。

## 步骤

### 第 1 步：先写失败的布局用例

新建 `frontend/e2e/layout.spec.ts`（视口 1440×900，先 `signIn`），用例：

1. `chat page opens with the rail and the conversation only`：在 `/`，`.forebrain-rail` 与对话 `<main>` 可见，`[data-testid="chat-workbench"]` 不存在；
   对话区所在列的右缘 = 视口宽度 ±1px；`[data-testid="chat-drawer"]` 不存在；`[data-testid="workbench-toggle"]` 的 `aria-pressed` 为 `false`；菜单栏里没有任何分组标题（"管控""工作区"文字不存在）。
2. `workbench opens as the third column and closes again`：点击 `[data-testid="workbench-toggle"]` → 工作台出现，三者 `boundingBox` 首尾相接（菜单栏右缘 = 对话区左缘 ±1px，
   对话区右缘 = 工作台左缘 ±1px，工作台右缘 = 视口宽度 ±1px）；截图 `layout-workbench-open.png`；点击工作台的关闭按钮 → 工作台消失；再打开后进入 `/agents` 再回到 `/`，工作台仍是打开的；
   `page.reload()` 后工作台收起。
3. `chat menu opens the drawer and new chat lives there`：点击菜单栏"对话" → `[data-testid="chat-drawer"]` 出现在菜单栏右缘（抽屉左缘 = 菜单栏右缘 ±1px），对话区宽度不变；截图 `layout-drawer.png`；
   点击 `[data-testid="drawer-new-chat"]` → 抽屉收起，地址栏出现 `?session=<id>`；再次打开抽屉，该会话处于选中状态；按 `Escape` 抽屉收起。
4. `sending a message and switching sessions`：在新会话里发送 `first-e2e` 并等到 `E2E_REPLY_OK`；再新建一个会话发送 `second-e2e`；打开抽屉点击第一个会话 → 地址栏 `session` 变为第一个 id，对话区出现 `first-e2e` 且不出现 `second-e2e`。
5. `drawer works from any page`：进入任一非对话页面，点击菜单栏"对话"打开抽屉，点击第二个会话 → 回到 `/` 且对话区出现 `second-e2e`。
6. `heartbeat is in the rail, not the workbench`：打开工作台，工作台内不包含心跳标题文字；点击菜单栏底部"心跳"→ 浮层出现；填 `30` 分钟与提示词"有新变化吗" → 点"启用" →
   打开抽屉，当前会话行出现 `[data-testid="drawer-heartbeat-indicator"]`；再点"清除" → 指示图标消失。浮层打开时截图 `layout-heartbeat.png`。
7. `empty live agents card is hidden`：没有子代理运行时，打开工作台，工作台内不存在"运行中的代理"卡片（按标题文字断言），`[data-testid="workbench-toggle"]` 上没有数字徽标。
8. `only one place switches the primary agent`：菜单栏顶部下拉选择器展开后列出全部主代理；页面上除该选择器外不存在任何"切换"按钮（全站文本断言）；
   菜单栏深色、浅色、折叠三种状态下分别展开下拉，其计算背景色依次为 `rgb(19, 36, 71)`、`rgb(255, 255, 255)`、`rgb(19, 36, 71)`，都不是 `rgba(0, 0, 0, 0)`；截图 `layout-tenant-menu-<dark|light|collapsed>.png`。
9. `rail collapses to icons and starts expanded`：初始菜单栏宽 264px ±1，`[data-testid="rail-collapse"]` 的 `aria-expanded` 为 `true`；点击后宽 64px ±1，菜单文字不可见、每个菜单图标按钮有 `aria-label`；
   依次悬停每个一级菜单图标，300ms 内 `[role="tooltip"]` 可见且文字等于该菜单名称、位于图标右侧（提示左缘 > 图标右缘）；用 `Tab` 聚焦"设置"图标同样出现"设置"提示；此时打开对话抽屉，抽屉左缘 = 64px ±1；截图 `layout-rail-collapsed.png`；进入其他页面再回来仍是折叠；`page.reload()` 后恢复展开。
10. `narrow rail keeps the drawer and the workbench overlays`：视口 800×900 时 `.forebrain-rail` 宽 64px，点击"对话"图标仍能打开抽屉；
   打开工作台后它覆盖在内容区上（对话区宽度与打开前相同 ±1px），按 `Escape` 关闭。
11. `run closes with the terminal's worked line`：任一轮回答结束后，结束行文字匹配 `^已工作 \d+s · \d{2}:\d{2}$`（假模型这一轮没有任务清单，不出现勾选图标），另在 `ChatWorkbench` 之外为结束行组件写单测：传入 `plan: { done: 3, total: 3 }` 时渲染一个 `svg`，其计算样式 `verticalAlign` 约等于 `-0.14 × fontSize`、宽高等于 `fontSize` ±0.5px；其中 `HH:MM` 等于浏览器本地当前时刻 ±1 分钟；切到英文后为 `Worked for …`；分隔线元素的计算样式 `backgroundImage` 为 `none`。
12. 截图：对话页默认状态（菜单栏深色、浅色各一张，含至少 2 个会话）`layout-chat-dark.png`、`layout-chat-light.png`；对话抽屉打开 `layout-drawer.png`；窄屏打开工作台 `layout-narrow.png`。

**验证**：`scripts/acceptance/web_e2e.sh` → 失败，失败断言来自 `layout.spec.ts`。

### 第 2 步：共享会话状态与地址栏驱动

实现设计第 3 条。

**验证**：`cd frontend && corepack pnpm test` → 通过（`useAgentRoster.test.ts` 已更新为不含 `live`）；`grep -rn "live: '1'" frontend/src` → 无输出。

### 第 3 步：对话抽屉与打平的菜单栏

实现设计第 2 条（除心跳按钮外）与第 7 条；删除 `ChatSidebar.vue`。为 `ChatDrawer` 写单测：传入 3 个会话、搜索词过滤、当前会话高亮、选中后发出关闭、创建失败时显示错误原文。

**验证**：`test ! -e frontend/src/components/ChatSidebar.vue`；`grep -rn "ChatSidebar\|sidebar.settings" frontend/src` → 无输出；`cd frontend && corepack pnpm test` → 通过。

### 第 4 步：心跳入口

实现设计第 4 条。为 `useSessionHeartbeat` 写单测（`load` 后指示状态为启用；`clear` 后为 `null`；切换会话时先清空旧会话的状态）。

**验证**：`grep -n "SessionHeartbeat" frontend/src/views/ChatView.vue frontend/src/components/chat/ChatWorkbench.vue` → 无输出；`cd frontend && corepack pnpm test` → 通过。

### 第 5 步：工作台与顶栏

实现设计第 1 条（工作台开合、顶栏开关、窄屏抽屉）与第 5、6 条。为 `useWorkbench` 写单测（初始为收起；`toggle` 两次回到收起）。为 `ChatWorkbench` 写单测：`records=[]`、`error=''` → 不渲染"运行中的代理"标题；`records` 有 1 条 → 渲染；`records=[]`、`error='boom'` → 渲染并显示 `boom`。

**验证**：`cd frontend && corepack pnpm test` → 通过；`grep -n "'Live agents'\|个 agents\|Workspace 文件" frontend/src/locales/index.ts` → 仅英文表中的 `'Live agents'` 一处。

### 第 5b 步：共享清单进度与结束行（设计 6b）

按设计 6b 实现引擎与前端两侧：
- `pkg/event` 新增 `PlanProgressOf`（含 `pkg/event/plan_progress_test.go`），`pkg/tui` 的清单进度实现改为转调它——`pkg/tui` 既有测试是第一道回归门，行为必须逐字不变；
- `TurnCompletedPayload`（`pkg/event/run_events.go:102`）加 `plan_done`/`plan_total`/`plan_active`，由引擎在运行结束时用 `PlanProgressOf` 基于本轮最后一次任务清单更新填入；`pkg/gateway/api_extra.go:976` 附近的历史消息记录同样返回这三个字段（从已保存的任务清单用同一函数计算），并补对应测试断言；
- 前端 `formatWorkedDurationLabel` 改为结构化返回、按 6b 的图标与对齐规范渲染（`SquareCheck`、`1em`、`vertical-align: -0.14em`、`--forebrain-success`），分隔线去渐变去大写。

**验证**：
- `CGO_ENABLED=1 go test -tags fts5 ./pkg/event/... ./pkg/tui/... ./pkg/gateway/... -count=1` → 全部 `ok`；
- `cd frontend && corepack pnpm test` → 全部通过。

### 第 6 步：真机验证

**验证**：`scripts/acceptance/web_e2e.sh` → 最后一行 `web e2e: PASS`；截图目录有第 1 步列出的 `layout-*.png`。把 `layout-chat-dark.png` 与 `layout-chat-light.png` 的路径写进 README 本计划状态说明。

## 完成标准（全部满足）

- [x] 全部 `ok`（event 0.4s / tui 41s / gateway 3.2s）
- [x] 176/176 通过
- [x] 已删除
- [x] 无输出
- [x] 无输出（死 import 一并清除）
- [x] `web e2e: PASS`（19/19，含 layout 8 用例）
- [x] 均无输出
- [x] README 状态行已更新

## STOP 条件

- "现状"摘录与代码对不上（尤其是 `ChatView.vue` 中 `onMounted` 与 `handleNewChat` 的逻辑）。
- 改为地址栏驱动后，发现 `useChatStream` 在 `switchToSession` 之外还有别的路径修改 `sessionId`，且与地址栏同步会造成循环跳转。
- 除 `ChatView` 外还有别的视图依赖 `route.fullPath` 变化来重建自身。
- `SessionHeartbeat.vue` 被 `ChatView` 以外的地方使用，改成共享状态会影响它。
- 需要修改"范围"与设计 6b 之外的文件或接口。

## 维护说明

- 会话的"当前是哪一个"只有一个来源：地址栏的 `?session=`。新增任何"打开会话"的入口都应当 `router.push`，而不是直接调用 `switchToSession`。
- 菜单栏的会话区与折叠分组共享有限高度：以后往菜单栏加新入口，优先放进折叠分组或底部行，不要挤占会话列表。
- 工作台只放随当前对话变化、且有内容时才出现的信息；低频功能（如心跳）的入口放在菜单栏底部。
- 工作台默认收起是 owner 的明确要求；不要为了"记住用户习惯"把开合状态写进 localStorage，除非 owner 改口。
- 工作台里出现需要用户注意的新信息时（例如运行中的代理），要在顶栏开关上给出提示，因为它默认是收起的。
