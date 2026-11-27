# Plan 391 · Step A 报告（复用接缝核实 · 传输抽象设计 · 权威组件对照表）

> 本文件是 Step A 的产物（只读分析，无源码改动）。计划号已取 **391**（389/390 已被并行会话
> 占用，`plans/` 与 `~/.forebrain/workspace/plans/*/*.md` 两条 drift 8 检查均无匹配）。
> 基线：worktree `.worktrees/vscode-chat-workbench`，HEAD `041a9627`（含 383）。
>
> 说明：本仓 `.gitignore` 第 77 行的 `plans/` 是**注释掉的**，`plans/` 实际被 git 跟踪
> （`plans/config-tui-prototype.html` 等已在库里）。因此本报告与原型稿在 worktree 里是
> **未跟踪文件**（`?? plans/391-…`），按 owner 裁决「执行者零 commit」不提交，交评审处置。

## 0. 机器检查结果

| 检查 | 命令 | 结果 |
| --- | --- | --- |
| drift 1 relay 冻结 | `shasum -a 256 editors/vscode/src/relay.ts` | `1f53fde3ec48eadc28d8059e5ad2f2b555feee1ab9ff683f9fff160fd0600939` ✅ |
| drift 2 383 容器键 | `grep -c '"secondarySidebar"' editors/vscode/package.json` | 1 ✅ |
| drift 3 manifest.test | `ls editors/vscode/src/manifest.test.ts` | 存在 ✅ |
| drift 4 解析器 | `grep -c parseForebrainRunEventMessage frontend/src/lib/forebrainGatewayRuntime.ts` | 1 ✅ |
| drift 5 品牌 token | `grep -c forebrain-brand-1 frontend/src/assets/main.css` | 24 ✅ |
| drift 6 ai-elements | `ls ai-elements-vue/packages/elements/src/conversation` | 存在 ✅ |
| drift 7 旧 iframe | `grep -c iframe editors/vscode/src/panelHtml.ts` | 3 ✅ |
| drift 8 计划号空闲 | `ls plans/ \| grep -cE '^391-'` / 跨 session `^# Plan 391\b` | 0 / 0 ✅ |
| Step A 基线 | `cd frontend && pnpm typecheck` | exit 0 ✅（首次 `pnpm install` 不带 frozen，3s 完成，lockfile 未变） |

## 1. 传输接缝（实读，行号为本 worktree 现状）

### 1.1 前端「两套传输」——这是抽象必须面对的事实

`lib/api.ts` 不是单一传输，而是**两条并存**：

| 传输 | 位置 | 形态 |
| --- | --- | --- |
| axios 实例 | `api.ts:48` `const API_BASE='/api'`；`:2559` `axios.create({baseURL: API_BASE, timeout: 600000})`；`:2566` `export const __apiClient = api`；`:2589` 起 `api.defaults.adapter` 做「同键读合并 + 驼峰化」 | 会话 cookie 由浏览器带上，401 走 `gatewaySession.reportGatewayUnauthorized()`（`:2623`） |
| 原生 fetch | `api.ts:2633 gatewayFetch()`（`fetch()` + 401 上报），全书 31 处调用（`:2658/:2717/:4595/:4654/:4885/...`），配套 `:2644 gatewayErrorText`、`putRaw/postJson` 系列（把拒绝体解析成 `GatewayHttpError.payload`） | 同上，但拒绝体当数据读 |
| WebSocket | `forebrainGatewayRuntime.ts:673 buildBrowserForebrainGatewayChatWsUrl()`（`window.location.host` + `/ws/chat`，cookie 鉴权） | 无凭据进 URL |

**注入点结论**：抽象必须同时覆盖 axios 层与 fetch 层，否则 webview 一半请求会打到 `127.0.0.1` 的
相对路径上。做法是**在 `lib/api.ts` 内部把「发一个 HTTP 请求」与「开一条 WS」收敛成两个可替换的
实现**，页面侧的 `forebrainApi.*`、`postJson`、`gatewayFetch` 调用签名全部不变。

### 1.2 消费方接缝（要改的前端文件）

| 文件 | 规模 | 接缝 |
| --- | --- | --- |
| `frontend/src/lib/api.ts` | 5256 行 | 见 1.1；`forebrainApi` 定义在 `:2852`，`export default forebrainApi` `:5256` |
| `frontend/src/lib/forebrainGatewayRuntime.ts` | 677 行 | 解析器全是纯函数（`:444 parseForebrainRunEventMessage`、`:239 parseCompactionEvent`、`:326 parseGoalEvent` 等）；唯一的浏览器接缝是 `:673` WS URL 构造 |
| `frontend/src/composables/useChatStream.ts` | 7334 行 | `@/lib/api` 在 `:2153`（模块中部 import）；`forebrainApi.` **30 处**，共 **24 个方法**：`chatMessages getSessionArtifacts sessionEvents sessionSubagentHistory runInput runQueuedInput runCancel sessionInterruptSend sessionMode cancelAutoContinue paidTierOffer promptSuggestion peersDirectory releasePeerMessage denyPeerMessage fileInfo subagentInput subagentQueuedInput subagentPause subagentInterruptSend subagentContext subagentBudget getModels`；直连 WS **3 处**：`:4124`（会话观察者，`bind_session`）、`:6277`（`switch_mode`）、`:6601`（发送轮，`start_run` / `resume_connection` / `cancel_run`）；`buildBrowserForebrainGatewayChatWsUrl` 4 次引用；`:4119`/`:6273` 已有 `typeof WebSocket === 'undefined'` 守卫（webview 里 WebSocket 存在，故**必须靠注入而不是靠守卫**） |
| `frontend/src/composables/useChatSessions.ts` | 169 行 | `:2` `import { forebrainApi }`；方法 `chatSessions chatSessionCreate chatSessionTitle` |
| `frontend/src/views/ChatView.vue` | 3547 行 | `forebrainApi.` **17 处 / 16 个方法**（`hostTrustSet projectServersConsent projectServersPending rewind rewindPreview rewindUndo filesUpload chatSession chatSessionCreate saveSessionModel sessionModel sessionPreset chatModelDefault projectsList slashCommands subagentCancel`）。webview 的视图层是新写的 shell，但**它引用的组件与 composable 走同一条注入**，所以这 16 个方法也要在接口里 |
| `frontend/src/lib/gatewaySession.ts` | — | `reportGatewayUnauthorized()` 是浏览器会话概念（401 → 去登录）。webview 没有 cookie，401 只能由适配器翻译成「宿主重连」，因此**它属于传输实现，不属于页面** |

### 1.3 传输接口（设计）

```ts
// frontend/src/lib/transport.ts（新增，纯类型 + 浏览器默认实现工厂）
export interface ForebrainTransport {
  /** 一次 HTTP 调用。path 以 '/api' 起（沿用现有调用点的字面量）。 */
  request<T>(req: { method: string; path: string; body?: unknown; signal?: AbortSignal }):
    Promise<{ status: number; data: T; headers?: Record<string, string> }>
  /** 拒绝体当数据读的那几条路径（putRaw/postJson）。 */
  requestRaw(req: { method: string; path: string; body?: unknown; signal?: AbortSignal }):
    Promise<Response>
  /** 二进制（附件上传 / 交付物下载），ArrayBuffer + 取消。 */
  requestBinary(req: { method: string; path: string; body?: BodyInit; signal?: AbortSignal }):
    Promise<{ status: number; body: ArrayBuffer; contentType: string }>
  /** 一条 WS。open 之后由调用方 send/close，事件仍在调用方归约。 */
  openSocket(target: 'chat'): ForebrainSocket
  /** 401 的处理权归实现：浏览器跳登录，webview 交宿主重连。 */
  onUnauthorized(handler: () => void): void
}
export function createBrowserTransport(): ForebrainTransport
```

- **浏览器默认实现不变**：`createBrowserTransport()` 内部就是现有 `axios.create` + `fetch` +
  `buildBrowserForebrainGatewayChatWsUrl`，逐字搬运，不重写归约；`lib/api.ts` 与 `useChatStream`
  改为在启动时取 `getTransport()`（默认返回浏览器实现），SPA 行为与现在逐字一致。
- **webview 实现**：`postMessage` 适配器，方法白名单，宿主加 Bearer，返回结构与浏览器实现同形。
  事件（`run_event`）在**宿主侧合帧**（≤1 帧、保序），webview 只按序归约，与现在 `onmessage`
  的处理路径同一个函数。
- **STOP 判据（照计划）**：若某处绕开 `forebrainApi`/`gatewayFetch` 直接 `fetch`/`WebSocket`
  （当前只有 useChatStream 的 3 处 WS 与 runtime 的 URL 构造），说明「浏览器默认不回归」做不到
  就停下报告，**不复制一份归约造第二套**。

### 1.4 信任 RPC 三分支（宿主侧，Step 2 实现）

宿主依赖只有 `vscode`，拿不到 `frontend/locales`，所以**文案由 webview 在 `askTrust` 请求里带上**
（`chat.trustTitle/trustBody/trustConfirm/trustOnce`，`locales/index.ts:1608-1614`，英文 `:4209-4215`）。

| 分支 | 判定 | 动作 | 结果 |
| --- | --- | --- | --- |
| 1 工作区内 | `openableDeliverables([{path: pickedRoot, title: ''}], workspaceFolders).length > 0` | `hostTrustSet(root, true)` | `resolve(true)` |
| 2 工作区外 | 同上为 false，且 `workspace.isTrusted` | `showWarningMessage(…, {modal:true})` 用 webview 带来的串 | 确认 → `hostTrustSet(true)` → `true`；取消 → `resolve(false)`（不发送） |
| 3 未受信任 | `!workspace.isTrusted` | 不发；`capabilities.untrustedWorkspaces: {supported:false}` 让三视图根本不加载 | 受限占位 |

- 边界复用 `editors/vscode/src/deliverableGate.ts:10-16`（`resolved === root || resolved.startsWith(root + path.sep)`，
  两侧都 `path.resolve`）。**桩调用、不改该文件**：它只 import `node:path` 与 `sessionState` 的类型，
  在扩展代码里直接调用即可。
- **兄弟前缀单测**（安全承重）：`/a/proj` 与 `/a/project` 互不匹配；`/a/proj` 与 `/a/proj/sub` 互相匹配。
- **写记录失败沿 web 放行**：`ChatView.vue:1416-1428` 的语义是「prompt is an added gate, not the
  mechanism」——`hostTrustSet` 失败 `catch` 后仍 `resolve(true)`，网关抖动不拦首次发送。
- webview 侧三处调用点改走 RPC：`ChatView.vue:1467`（`hostTrustSet` 写记录）、`:1407`（`askTrust`
  Promise）、`:1420`（确认后写）——沿用同一控制流，只换实现。

### 1.5 consent 面板内复用

`ProjectServersConsentDialog.vue` 原样在 webview 里渲染：触发 `forebrainApi.projectServersPending`
（`ChatView.vue:3158-3166`，有行即开）→ 提交逐行 `{name, fingerprint, allow}` + `alwaysAllow`
（`:3184-3192`）→ 成功后重新读一次待确认列表并 `rebindSessionObserver()`。**组件零改动**，
只需要 `projectServersPending` / `projectServersConsent` 走注入的传输。

## 2. 构建 / 主题接缝（实读）

- **ai-elements 导入**：chat 组件全部走 workspace 别名式 specifier（`@repo/elements/message` 8 处、
  `@repo/elements/tool` 2、`@repo/elements/plan` 2、`@repo/elements/chain-of-thought` 3、
  `@repo/elements/prompt-input` 2、`@repo/elements/file-tree` 2、`@repo/elements/task` 2；
  shadcn 侧 `@repo/shadcn-vue/components/ui/button` 11、`input` 5、`scroll-area` 4、`checkbox` 2、
  `alert` 2）。`frontend/vite.config.ts:36-40` 把 `@repo/elements → ai-elements-vue/packages/elements/src`、
  `@repo/shadcn-vue → ai-elements-vue/packages/shadcn-vue`，并 `dedupe: ['nanoid','vue']`
  （`:41`）、`optimizeDeps.include: ['nanoid']`。**webview 的 vite.config.ts 必须逐条复制**
  （含 `__FOREBRAIN_VERSION__` define 与 `mermaid` 的不可用替身），否则两套构建解析出两份 Vue。
- **复用 main.css**：`frontend/src/assets/main.css` 结构＝`:root` 亮色 token（`:5-83`）→
  三 scheme（`:84` navy / `:102` teal / `:115` graphite）→ rail 主题（`:133-260`）。
  `html.dark` 落点＝**在三 scheme 之后新起一段 `html.dark{…}`**（与 `tailwind.config.js:10`
  `darkMode:'class'` 吻合）；web 整站不设 dark 类，`main.css` 现有 `always-white` 语义不动。
- **tailwind content**：`frontend/tailwind.config.js:3-8` 已包含 `../ai-elements-vue/packages/{elements,shadcn-vue}/…`。
  webview 落地时**同一份 config** 再加 `editors/vscode/webview/src/**/*.{vue,ts}`（Step 3），
  否则原子类会被裁掉（构建产物 CSS 断言就是查这个）。
- **包管理**：`frontend/pnpm-workspace.yaml` packages 只有 `.` 与两个 ai-elements 包；
  webview 加入后 `frontend/pnpm-lock.yaml` 的 importers（现 `:.`、`../ai-elements-vue/…`）必然新增一段。
  本 worktree 已有 `frontend/node_modules`（`pnpm install` 3s 完成，lockfile 无 diff）。

## 3. 权威组件对照表（口径＝ChatView.vue 引入的全部 .vue）

`frontend/src/views/ChatView.vue` 共 **52** 个 `.vue` 导入（`grep -nE "^import .*from '.*\.vue'"`，
行 833-943）。**纳入 51，deferred 1**。截图目录：`plans/391-shot-light-<场景>.png`
（`PROTOTYPE_SHOTS=1` 生成）。

| # | 组件 | 引入行 | 原型场景 | 实施步骤 |
| --- | --- | --- | --- | --- |
| 1 | ProjectPill | 833 | empty | 9（空态与目录） |
| 2 | FolderDialog | 834 | empty（点药丸） | 9 |
| 3 | RewindDialog | 835 | plan（回到这里） | 8 |
| 4 | TrustDialog | 836 | **不画（deferred）** | — |
| 5 | ProjectServersConsentDialog | 837 | consent | 8（面板内复用） |
| 6 | SessionHeartbeat | 838 | runtime | 8 |
| 7 | BadgeComponent | 839 | thread（交付物计数） | 5 |
| 8 | SubagentCallCard | 840 | subagent | 8 |
| 9 | SubagentDrawer | 841 | subagent | 8 |
| 10 | SubagentTab | 842 | subagent | 8 |
| 11 | RunStatusLine | 843 | thread / runtime | 9（状态行） |
| 12 | SlashPickerCard | 844 | slash | 6 |
| 13 | ActivityLine | 845 | thread / fail | 5 |
| 14 | ActivityStep | 846 | thread / fail | 5 |
| 15 | PeerMessageCard | 847 | peer | 8 |
| 16 | PeerDeliveryCard | 848 | peer | 8 |
| 17 | PeerHeldBanner | 849 | peer | 8 |
| 18 | PeerDrawer | 850 | peer | 8 |
| 19 | PeerMark | 851 | peer | 8 |
| 20 | PlanUpdateCard | 852 | plan | 8 |
| 21 | ToolCallCard | 853 | thread / fail / subagent | 5 |
| 22 | ToolDocCommand | 854 | thread | 5 |
| 23 | ToolDocOutput | 855 | thread | 5 |
| 24 | ToolDocTitle | 856 | thread | 5 |
| 25 | ThinkingCard | 857 | thread | 5 |
| 26 | ApprovalCard | 858 | thread / fail | 7（内联审批） |
| 27 | PlanApprovalDock | 859 | plan | 8 |
| 28 | BatchRunActions | 860 | runtime | 8 |
| 29 | McpNotice | 861 | runtime | 8 |
| 30 | ComposerPlusMenu | 862 | slash | 6 |
| 31 | ModeChip | 863 | 全部 | 6 |
| 32 | UserAttachments | 864 | thread / slash | 6 |
| 33 | ModelChip | 866 | 全部 | 6 |
| 34 | EffortChip | 866 | 全部 | 6 |
| 35 | ContextRing | 867 | 全部 | 6 |
| 36 | ForebrainPromptTextarea | 899 | 全部 | 6 |
| 37 | ChatWorkbench | 900 | 全部 | 3（shell） |
| 38 | ApprovalPresetPicker | 901 | 全部 | 6 |
| 39 | ViewModeChip | 902 | 全部 | 6 |
| 40 | RunWorkedLine | 903 | fail / runtime / plan | 8 |
| 41 | ArtifactList | 904 | thread | 9（交付物） |
| 42 | ArtifactPreview | 905 | thread | 9 |
| 43 | RunErrorBlock | 906 | fail | 9（错误态） |
| 44 | PendingInputQueuePopover | 907 | thread | 6 |
| 45 | AutoContinueBanner | 908 | runtime | 8 |
| 46 | PaidTierBanner | 909 | runtime | 8 |
| 47 | LspRecommendationCard | 910 | runtime | 8 |
| 48 | DiffView | 911 | thread | 5 |
| 49 | CompactionCard | 912 | runtime | 8 |
| 50 | GoalLine | 913 | runtime | 8 |
| 51 | PlanDoc | 931 | plan | 8 |
| 52 | AttachSaveDialog | 943 | slash | 6 |

统计：Step 3 = 1，Step 5 = 9，Step 6 = 12，Step 7 = 1，Step 8 = 22，Step 9 = 6，deferred = 1。

**deferred 理由（照计划）**：
- `TrustDialog`（836）：面板内不画，信任交给 VS Code Workspace Trust＋宿主三分支（1.4）。
- Sessions 删除：网关无 DELETE，后端零改动，列表只做新建/搜索/切换。

**传递依赖（不单列，经上表组件进入）**：`ToolGateFacts`/`ToolGateFooter`（ApprovalCard 内部）、
`PlanProgressSegments`（RunStatusLine 内部）、`SubagentConversation`（SubagentTab 内部）、
`DiffBlock`（DiffView 内部）、`ForebrainModal`（三个弹窗内部）——它们跟着父组件的步骤与截图走。

## 4. 原型稿（Step 0 交付物）

- `plans/391-vscode-chat-workbench-prototype.html`：17 个可点场景（`thread fail runtime plan
  handoff-models handoff-effort review-models review-running review-verdict subagent peer consent
  slash sessions empty api fallback`），覆盖上表 51 个纳入组件；顶部两个工具栏：**主题**
  （Light / Dark）与**品牌 scheme**（Navy / Teal / Graphite）。
- 计划两种去向的页面（owner 2026-11-26「交给其他模型执行和请其他模型评审的页面原型也必须画
  出来」）：`PlanApprovalDock` 的页脚本来就是两屏，原型照实画了五个场景——交给谁执行（标题
  「交给哪个模型执行？」＋「在新的 worktree 里实施，不动当前分支」＋每行 provider/model 与其
  endpoint＋安装里配好那行的高亮＋「当前」）、选思考强度（「返回选模型」＋「<模型> 用哪档思考
  强度？」＋这个模型真正提供的档位，模型行自己配的那档是打开的选中项并带「模型设置」齿轮、
  行里写档位 id）、请谁评审（同一套行，标题「用哪个模型评审这份计划？」，没有 worktree 那句）、
  评审进行中（「<模型> 正在评审这份计划…」＋停止；评审自己的进度在时间线里的 plan-reviewer
  子代理卡上）、评审结论（「<模型> 的评审结论」＋结论正文，页脚回到四个去向）。按钮到页面的
  连线在原型里可点：计划 dock 的两个按钮 → 选模型 → （交给：选强度并提交／评审：进行中 → 停止
  折回），回归里逐屏断言。
- 计划卡的小眼睛（owner 2026-11-26「点击计划卡小眼睛图标自动在 VS Code's editor 区域打开计划
  文件」）：不再是卡内的「显示全部」，而是 `title/aria-label = 在编辑器中打开计划`，点一下在
  **中间编辑区**（VS Code 的 editor area，非 webview）打开 `plans/391-vscode-chat-workbench.md`
  的 markdown 预览——编辑区多出一个按文件名命名的预览页（`预览` 角标、斜体标题，与代码页
  `panel.ts / package.json / relay.ts` 并列），正文是标题、路径与状态、目标、五条步骤、闸门，
  与卡片里的 `PlanDoc` 同一份 `PLAN_DOC` 数据（卡在 chat webview 里不动）。再点一次是「已经
  在编辑器里打开」的空操作，点代码页切回源码，预览页签在切走后仍留着。回归逐项断言并留
  `plans/391-shot-light-plan-editor.png`。
- 计划卡（owner 2026-11-26「为什么要加计划已更新卡，web端有吗？」）：web 端确实有
  （`PlanUpdateCard.vue`，session_todo 渲染的那张），原型的形状是错的，已按组件改对——
  brand 发丝线＋10px 圆角＋`10px 14px` 内边距＋13px，标题一行、可选一句说明，然后是一张清单：
  每条＝状态字形＋正文（`✓` completed、`▣` in_progress（正文加粗）、`✕` cancelled（字形 danger）、
  `□` pending，其余字形用品牌色）。**卡里没有任何计数**（组件注释写明「the progress a tally would
  repeat is already printed above the composer」），进度只在上方运行行（`RunStatusLine` 3/6）。
  新增的固定断言逐项钉住字形序列、状态序列、加粗、danger 字形、无 `n/m`、无「完成」「新增」「调整」
  与上面那套 chrome。
- **标题口径的一处纠正**（审计后按代码落地）：卡片标题不是「计划已更新」。引擎发的是英文常量
  `'Updated Plan'`（`pkg/tool/session_tools.go:268`），web 的解析器用
  `ENGINE_TITLES{'Updated Plan' → chat.updatedPlan}` 过一遍 `translate`（`useChatStream.ts:2386-2387`
  与 `:2421`），而 zh 的 `chat.updatedPlan` 是 **更新待办**（`locales/index.ts`）。所以中文界面里这张
  卡的标题是「更新待办」，原型与测试都照这个值。若 owner 希望它读「计划已更新」，那是改
  `chat.updatedPlan` 一处文案的事，等一句话。
- 术语统一（owner 2026-11-26 裁决）：session_todo 这个概念在 web/TUI/引擎里是 zh「更新待办」/
  en「Updated Plan」（`chat.updatedPlan` zh=更新待办 en=Updated Plan；引擎常量 `'Updated Plan'`；
  TUI `render.go` 同一个词），唯一走样的是这份原型。已改：面板内那张卡的表头、聊天头部的开关
  tooltip、`plan` 场景说明、以及切换时写进交互记录的那一行；`grep 任务清单` 在原型里 = **0**，
  对话里的计划卡本来就是「更新待办」。测试改成断言「更新待办」并加一条守卫断言（旧词不得回到
  这个面）。仅用户可见的中文标题与说明改动，类名（`.todo` / `PanelChecklist` / `toggle-checklist`）
  一个没动。
- 待办（三条合并裁决 + 两处收尾，2026-11-26，先后覆盖，最终形态就这一种）：
  1. **不是 VS Code 原生 Tree View**（owner「不要 reuse VS Code's native Tree View，对齐 claude
     code，设计和渲染自己的待办列表」）。原生树的实现尝试已整块撤掉：`.vtree-*`、22px 行、
     缩进线、`--vscode-*` 令牌一起删除，`--vscode-*` 在这份原型里再无消费者。
  2. **时间线里不显示 webview 自己渲染的待办消息卡**（owner「vscode插件里不要显示」）：
     `PlanUpdateCard` 那张字形卡（✓▣□✕ + 说明行 + 无计数）在**每个场景**都不再出现，
     `planUpdate()`、`.plan-card*`、它在 `SESSION_TODO` 里的 title/explanation 一并删除；
     它退回 web-only（`frontend/src/components/chat/PlanUpdateCard.vue` 仍在 web 用）。
     `PlanApprovalDock`（计划闸门 + 评审结论）与 `PlanDoc`（计划文件卡 + 眼睛开编辑器）**保留**，
     它们是另外两个面。
  3. **待办更新必须有提醒**（owner「待办列表如果有更新，必须要有可以提醒用户的UI效果」）：
     观察器里有「模拟一次 session_todo 更新」按钮（也暴露成
     `window.chatWorkbenchPrototype.updateTodos()`），一次推进两项（进行中→完成、下一个待办→
     进行中）。列表**收起**时，composer 的芯片长出未读角标（数字）＋脉动圆点，并把「N 项有更新」
     写进 `aria-label`；列表**展开**时，变化的行闪一下（左边一道 brand 竖线 + 浅底，2.6 秒自行
     退场），头部出现「已更新」小标签。打开列表即清零角标。
  4. 形态与位置：**一张卡，落在输入框正上方**（owner「必须显示在输入框上方，不要显示在顶部」），
     由 composer 芯片开/关（Claude Code 的 `app:toggleTodos` 形态，会话作用域，`aria-expanded`）。
     样式回到 owner 偏好的那张「任务清单」卡：品牌发丝线、10px 圆角、`--alt` 底的头（图标 + 标题 +
     计数）、行内小状态图标（完成 ✓ 绿 / 进行中 ○ brand 加粗 / 待办 ○ 灰 / 取消 ✕ 红 + 划线）、
     完成行变淡划线；全部 forebrain 令牌，是 webview 面。
  5. 文案与极简收尾：标题与芯片都用 **web 自己的词「待办」**（`OverviewTab.vue:19` 的待办卡标题
     `chat.benchTodos` = 待办，计数写作 done/total；`chat.checklistProgress` 只是进度条 aria-label），
     芯片带 `2/6`；行内**删掉了右侧的 Step 标签列**（owner 截图圈注：不需要），一行只留图标与正文。
  计数、角标、列表三者同源（`SESSION_TODO.items`），不会再出现两个互相打架的数字。
- 视觉口径：`main.css` 亮色 token 照抄；暗色 token 为本计划**提案定稿**（`#vsc.dark` 作用域，
  纸面外框保持亮色——即「web 整站仍亮色」在原型里就可验证）；成败＝`--ok`/`--danger` 同源。
- 三 scheme **改的是全应用强调色族**（owner 裁决，2026-11-26 修入本稿）：`main.css:80-82`
  写明 “content-area accents and the rail's buttons and marks all read from these, so switching
  recolours everything in unison”，所以内容区（按钮、审批衬线、mention/slash 芯片、交付物链接、
  上下文环、运行态点线）与侧栏、活动栏标一起换。实现＝把 `--brand / --brand-hover / --brand-soft /
  --brand-line` 落在窗口根（`#vsc`）上，两块 webview 继承同一族；测试逐值断言三 scheme 下
  侧栏圆点、mention 墨色、审批主按钮底色、交付物链接色、运行态转圈的 `border-top` 都等于
  `main.css` 声明的那三个值（dark 下换成提亮版并再断言一次）。
  （前一版「只改侧栏、聊天面固定 navy」的做法已被本裁决推翻，本文件与原型同步改正。）
- 截图：`plans/391-shot-*.png` 由 `PROTOTYPE_SHOTS=1` 生成（含 `-teal-thread` 与 `-graphite-thread`，
  用来看内容区强调色的换色），**只是评审用的产物，不要提交**
  （`plans/` 在本仓实际被跟踪；两份真正的交付物是 `.html` 与 `.test.cjs`）。
- 审批预设的中文口径（owner 裁决「web 端代码也要改一下，对齐」）：zh 侧两项由英文改为
  **工作区写入**（`locales/index.ts` 的 `settings.preset.workspaceWrite` 与 `approval.default`；
  `完全访问` 已是中文），en 侧 `Workspace write` 不动；原型里 composer 芯片与
  ApprovalPresetPicker 的行同口径（两行：工作区写入与完全访问，见下一条），并加断言「任何中文
  场景都不再出现 Workspace write」。受影响的固定断言同步改：`SettingsApprovalTab.test.ts`、
  `settingsSchema.test.ts` 读的 `src/lib/testdata/config-db-settings-preview.html`、
  zh-pinned 的 `e2e/settings-tabs.spec.ts`；`ApprovalPresetPicker.test.ts` 与
  `e2e/subagent-drawer.spec.ts` 是 **en** 区域（`setLocale('en')` / `forebrain-locale=en`），
  它们的 `Workspace write` 断言保持原样。
- 审批预设列表只列具体预设（owner 2026-11-26 裁决，附截图）：「跟随默认」这一项删掉，默认预设
  自己就是选中并回显的那一行。落地两处：①原型菜单只有 **工作区写入（当前）与完全访问**，芯片
  回显 工作区写入（`PRESETS` 两项 + `DEFAULT_PRESET`）；②web 端 `ApprovalPresetPicker.vue` 的
  `currentLabel` 不再回落 `t('approval.followDefault')`，读取未回与读取失败两种时刻都回显「本机
  默认的那个具体预设」（`presets[0]`；frozen 时它就只剩 full_access），菜单本来就只有两项、未动。
  新增两条 en 区域测试钉住这两个时刻：旧实现显示 `Follow default` 时**实测 2 条失败**，修后
  `ApprovalPresetPicker.test.ts` 9/9 通过。`approval.followDefault` 因此在本仓再无消费方
  （`grep -rn followDefault frontend/src` 只剩 locale 定义），按「不破坏其它消费方」保留未删。
- 回归：`plans/391-vscode-chat-workbench-prototype.test.cjs`（仿 `plans/381-…test.cjs`），
  `python3 -m http.server 8765 --bind 127.0.0.1 --directory plans` 之后
  `node plans/391-vscode-chat-workbench-prototype.test.cjs` → **17/17 通过，浏览器零报错**。
  另断言：脚本可解析、`connect-src 'none'`、全文无 `·`、无 `dashed` 占位、`TrustDialog` 不出现。

## 5. Step A 的结论

1. 传输抽象可行，且**必须同时覆盖 axios 与 fetch 两条路**（1.1）；页面侧调用签名不变，浏览器默认
   实现逐字搬运，不重写归约 —— 满足「SPA 不回归」的 STOP 条款。
2. 唯一需要 `typeof WebSocket` 之外手段的地方是 useChatStream 的 3 处直连 WS 与 runtime 的 URL 构造，
   共 4 个接缝，全部收敛到 `ForebrainTransport.openSocket()`。
3. 信任三分支、consent 复用、`deliverableGate` 边界桩调用都有现成实现可接，不需要新概念。
4. 对照表 51 纳入 / 1 deferred，与 prototype 的场景一一对得上（测试机器校验）。