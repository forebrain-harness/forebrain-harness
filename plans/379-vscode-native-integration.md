# Plan 379: VS Code 原生集成——`session_todo` 清单树、状态栏、原生审批通知、选区/文件送会话、交付物打开

> **Executor instructions**: Follow this plan step by step. Run every
> verification command and confirm the expected result before moving to the
> next step. If anything in the "STOP conditions" section occurs, stop and
> report — do not improvise. When done, update the status row for this plan
> in `plans/README.md` — unless a reviewer dispatched you and told you they
> maintain the index.
>
> **Precondition**: [376](376-vscode-extension-chat-panel.md) 已 DONE 并落在本 worktree 的基线上
> （`editors/vscode/` 存在且其单测全绿）。未落地即 STOP。
>
> **Drift check (run first)** — 任一不成立即 STOP：
>
> ```bash
> git status --porcelain -- editors/vscode            # 无输出（376 已提交进基线；执行器不提交）
> cd editors/vscode && npm ci --no-audit --no-fund && npm test   # exit 0，`# tests 54`、`# fail 0`
> grep -c "onRequest?: (method: string, path: string) => void" src/relay.ts   # 打印 1
> cd ../.. && git diff 3a261297 -- pkg/gateway/ws_protocol.go pkg/gateway/server.go pkg/gateway/approval.go \
>   pkg/gateway/wsevents.go pkg/gateway/api_extra.go pkg/gateway/session_files.go pkg/event \
>   frontend/src/views/ChatView.vue frontend/src/App.vue frontend/src/lib/api.ts   # 无输出
> ```
>
> 本计划盖章于 `3a261297`（dev，2026-11-25）；上一条 `git diff` 查本计划引用的 harness 文件是否在盖章后变过。

## Status

- **Priority**: P1
- **Effort**: L
- **Risk**: MED
- **Depends on**: [376-vscode-extension-chat-panel](376-vscode-extension-chat-panel.md)（面板、中继、token 梯子、`Connection`）
- **Category**: direction（编辑器集成新功能）
- **Planned at**: commit `3a261297`, 2026-11-25
- **Status**: TODO
- **后续**: 380（原生 Chat 参与者 `@Forebrain`，未起草）

## Why this matters

376 把**全部特性**的 Web UI 嵌进了右侧面板；但 owner 要的是「类 Cursor 且更顺」——光有一个嵌进去的网页还不够，
编辑器与对话之间得有原生通道。本计划补上这一层，全部只用 VS Code 稳定 API、harness 零改动：

1. **`session_todo` 清单树**：代理每调一次 `session_todo`，VS Code 里就有一棵原生树实时反映
   「待办／进行中／已完成」（按代理分组，子代理的清单单独一节），状态栏常驻 `3/7` 进度。owner 2026-11-24 点名要求。
2. **原生审批通知**：面板没开着时，审批请求（`approval_requested` 事件）弹 VS Code 通知，Approve／Deny 不用切回面板。
3. **选区／文件送会话**：编辑器右键或 `Ctrl+Cmd+K`（Win/Linux `Ctrl+Alt+K`，owner 2026-11-24 裁决）把选区连同你的问题
   送进**面板当前显示的会话**（owner 裁决：默认进当前会话，设置可改为总是新会话）。
4. **交付物打开**：一轮结束时通知「交付了 N 个文件」，一键在 VS Code 里打开（仅限工作区内路径）。

对话本身、审批卡全文、`user_interaction` 问题卡、plan 模式、子代理视图、跨进程代理通信，仍然全部在面板的 Web UI 里
（376 已交付）——本计划不在原生侧复制任何一份 UI，只做「镜像 + 入口」。

## Current state（全部为 advisor 在 `3a261297` 实读核实的事实）

**WS 协议（扩展宿主这一侧要说的语言；与 376 不同，本计划真的要说 WS）**
- 端点 `/ws/chat`；握手带 `Authorization: Bearer <token>` 即通过（`pkg/gateway/controlplane.go:80-95`）；
  **不要给 `ws` 客户端设置 `origin` 选项**——gorilla 默认 CheckOrigin 对无 Origin 的握手放行（`pkg/gateway/server.go:829`
  `var upgrader = websocket.Upgrader{}`）。
- 服务端在升级完成后**主动**发 `connected`（`server.go:1566-1571`），客户端不必先发 `connect`（发了也只是再收一次，`:1801-1804`）。
- 客户端信封 `wsClientMsg` 的 json tag（`server.go:831-855`）：`op, protocol_version, type, request_id, run_id, action_id,
  session_id, agent_id, mode, answers, approval_decision, request, message{content, role, attachments, mention_images, …}, content, cursor`。
- 服务端信封 `wsServerMsg`（`:878-894`）：`op, request_id, run_id, session_id, trace_id, text, message, data, error`，三个结束 op
  另带 `plan_done, plan_total, plan_active`（`event.RunPlanFacts`，`pkg/event/run_events.go:231-235`）与 `artifacts`
  （`event.RunArtifacts`，`:261-263`）。
- 校验规则 `validateWSClientMessage`（`pkg/gateway/ws_protocol.go:150-215`）：`protocol_version` 可省略，带了必须等于
  `"2026-04-18"`（`:13`）；`wsOpsRequireRequestID`（`:73-83`）里的 op 必须带 `request_id`——本计划会发的 `bind_session`、
  `start_run` 都在其中；`approve_action|deny_action|submit_answer` 必须带 `action_id`。
- `bind_session {request_id, session_id, cursor}`（`server.go:1805-1865`）→ 回 `session_bound`（`data: {cursor, high_water,
  schema_version}`），随后把 `cursor < sequence ≤ high_water` 的历史事件逐条以 `run_event` 回放，再转实时；
  历史快照不可用时回 `run_warning` + `message: "session_event_snapshot_unavailable"`。
- `run_event` 帧：`wsServerMsg{op:"run_event", data: event.RunEvent}`；`RunEvent` 的 json（`run_events.go:151-165`）：
  `{id, sequence?, schema_version?, run_id?, session_id?, type, payload?, created_at}`。本计划消费的 `type`（`run_events.go:10-135`）：
  `turn_started`、`turn_completed`、`turn_cancelled`、`turn_error`、`turn_failed`、`tool_call_started`、`tool_call_completed`、
  `approval_requested`、`mode_changed`、`plan_updated`、`subagent_spawned`、`subagent_ended`。
- `plan_updated` 的 `payload`（`PlanUpdatedPayload`，`run_events.go:741-759`）：
  `{title, explanation, completed, total, items: [{id, content, status, active}], active, agent_id}`——`agent_id` 为空＝主代理，
  否则是该子代理的清单。`session_todo` 的调用在所有表面上都渲染为这张清单卡而不是工具卡（`PlanEmittingTool`，`:765-771`），
  Web UI 亦然（`frontend/src/composables/useChatStream.ts` 处理 `plan_updated`）。
- 审批与结束有**两套通道，给两种连接**（`server.go:1426-1454`：`writeMsg` 只写给发起该 op 的那条连接；`wsevents.go:55-131`：
  由它派生的 `run_event` 才扇出给所有已绑定的连接）：
  - **发起者专属 op**（只有发 `start_run`/`approve_action` 的那条连接收到）：`requires_action`（`server.go:2832-2839`、
    `approval.go:336-343`）`{op, request_id?, run_id, session_id, message, data: {id, kind, action_id, action_kind, agent_id?,
    subagent_type?, permission_suggestion?: {permission_tool_name, permission_input, …}}}`；`action_approved | action_denied |
    action_cancelled`（`server.go:1727-1745`）；`run_completed | run_cancelled | run_error`（顶层带 `plan_done, plan_total,
    plan_active` 与 `artifacts`）。
  - **所有已绑定连接都收到的 `run_event`**（`wsServerMsg{op:"run_event", data: event.RunEvent}`；`RunEvent` 的 json 见
    `run_events.go:151-165`：`{id, sequence?, schema_version?, run_id?, session_id?, type, payload?, created_at}`）。本计划消费的
    `type`：`turn_started`、`turn_completed`、`turn_cancelled`、`turn_error`（payload 内嵌 `RunPlanFacts` 与 `RunArtifacts`，
    `run_events.go:265-282`）、`tool_call_started`、`tool_call_completed`、`approval_requested`（payload
    `ApprovalRequestedPayload{action_id, action_kind, agent_id?, subagent_type?, permission_suggestion?}`，
    `pkg/event/approval_events.go:30-42`）、`plan_updated`、`subagent_spawned`、`subagent_ended`。
  - **宿主 WS 是「观察者」**：它只 `bind_session`、不发 `start_run` 去跑面板里的轮次，所以面板里发起的轮次（主场景）只会以
    `run_event` 到达——归约器必须以 `run_event` 为主、发起者专属 op 为辅（见 Step 1）。没有 `approval_resolved` 这类事件：
    审批被答掉要靠 REST 调和或轮次结束来清（Step 5）。
- `start_run {request_id, protocol_version, session_id, message: {content, role: "user"}}`（`message.content`、附件**与 `session_id`
  三者皆空**才被拒，`:197-213`；本计划总带 `session_id` 与正文）→ `run_started {request_id, run_id, session_id}`（`:2512-2516`）；
  被拒时回 `turn_withdrawn`／`turn_refused`（带 `error`，部分带 `data.code`）。会话忙的 code 是四个之一：
  `session_running`／`session_awaiting_approval`／`session_running_elsewhere`／`session_awaiting_approval_elsewhere`
  （`state.SessionBusyCode`，`pkg/state/runrt.go:58-71`；发送点 `server.go:2492-2495`）——**没有 `session_busy` 这个 code**。
  运行在服务端继续，与发起它的 socket 无关（`resume_connection` 的存在即为此）。
- **同一会话多个连接各自独立订阅、互不踢**：`handleChatWS` 每条连接有自己的 `runEvtSub`/`Notifier.Subscribe`
  （`server.go:1301-1312`、`:1836-1853`），全包无任何「踢掉旧连接」逻辑（advisor grep `kick|evict|superseded connection` 零命中）。
  面板里 Web UI 自己的 WS 与扩展宿主的 WS 可以同时绑一个会话。**这是本计划的事实依据，不是假设**；但仓内没有显式的
  「双连接」测试，所以验收清单第 3 条（选区送进面板正在显示的会话——此时两条 WS 同绑一会话）专门盯它（见 STOP conditions）。

**REST 面（宿主侧用 Bearer；均经 `authorizeControlPlane` 头部凭据放行）**
- `GET /api/v1/projects` → `{"projects": [{id, name, root, project_key, …}]}`（`api_extra.go:7118-7143`，行形状 `projectRow` `:7054+`）。
- `GET /api/chat/sessions?project_id=<id>` → `{"records": [{id, title, create_time, update_time, …}], …}`，最新 100 条
  （`api_extra.go:1665-1727`）。
- `POST /api/chat/sessions`，body `{"title", "folder"}`（**不要带 `source`**：白名单只有 `""` 与 `"workshop"`，`api_extra.go:1490-1495`）
  → `201 {"id", "title", "project", "created"}`（`createSessionResult`，`:1424-1429`）；`folder` 会为该目录 find-or-create 项目
  （`:1527-1540`），跑批类项目回 `409 batch_project_uses_tasks`。
- `GET /api/actions?status=pending&session_id=<id>&limit=50` → 数组 `[{id, session_id, kind, status, payload_json, agent_id?,
  subagent_type?, permission_suggestion?}]`（`api_extra.go:4261-4307`）。
- `POST /api/actions/:id/approve`（body `{"decision":"accept"}` 可省，`:4423-4472`）／`POST /api/actions/:id/deny`。
- `GET /api/chat/sessions/:id/artifacts?page=&limit=` → `{"items": [{id, name, title?, description?, path, rel_path?, mime, kind, size, …}], "total"}`
  （`session_files.go:282-306`；`Artifact` 形状 `run_events.go:240-259`）。

**Web UI 的会话深链（面板切会话用）**
- 「地址栏是哪个会话打开的唯一来源」：`frontend/src/views/ChatView.vue:3106-3120` `watch(() => route.query.session, …)`；
  `App.vue:467-471` 的 `chatSessionId` 读 `route.query.session`。⇒ 把 iframe 的 src 换成 `<中继 origin>/?session=<id>` 即可让面板打开指定会话。
- SPA 打开会话时会 `GET /api/chat/sessions/<id>/messages`（`frontend/src/lib/api.ts:3058`）与 `/events`（`:3137`）
  ⇒ 376 的中继 `RelayOptions.onRequest(method, path)` 能观察到「面板此刻在看哪个会话」。

**VS Code 稳定 API（`@types/vscode@1.100.0` 起可用，本机 VS Code 1.147）**
- `vscode.window.createTreeView(id, {treeDataProvider, showCollapseAll})`、`TreeItem`（`iconPath: ThemeIcon`、`collapsibleState`、`tooltip`、`description`）；
  `contributes.viewsWelcome` 在树为空时显示说明（配 `setContext` 布尔上下文键）。
- `vscode.window.createStatusBarItem(id, alignment, priority)`；`text` 可含 `$(icon)` codicon（`$(sync~spin)` 会旋转）。
- `vscode.window.showWarningMessage(msg, ...items)` / `showInformationMessage` / `showInputBox` / `showQuickPick`。
- `WebviewView.visible`（面板是否可见）；`vscode.commands.executeCommand('forebrain.chat.focus')`。
- `vscode.workspace.getWorkspaceFolder(uri)`（交付物路径白名单）、`vscode.workspace.asRelativePath(uri, false)`、
  `vscode.languages.getDiagnostics(uri)`。
- `contributes.keybindings` / `menus`（`editor/context`、`explorer/context`、`view/title`、`commandPalette` + `when`）。

**376 交付的接缝（本计划要接的地方，均在 `editors/vscode/src/`）**
- `relay.ts`：`startRelay(target, { nonce, boot, preferredPort, onRequest })`——`onRequest(method, path)` 目前只用于可选日志；
  `path` 含查询串（原样来自请求行）。
- `connection.ts`：`Connection` 持有 token／relay／自起的网关；`emit(state: PanelState)` 推给面板；`reconnect()`；
  `currentToken()`（空串＝未连接）；`get origin()`；`start()`／`dispose()`。
- `panelHtml.ts`：`PanelState = {connecting} | {ready; relayOrigin; boot} | {unavailable; …}`；`panelUrl(relayOrigin, boot, session?)`
  （会话深链 `&session=<id>` 由它拼）；`renderPanelHtml(state, nonce)`。`panel.ts` 的 `ChatPanel.setState()` 重建 HTML。
- `extension.ts`：模块级 `active: Connection | undefined`；`activate` 里组装 `ConnectionDeps` 并注册命令。
- `gateway.ts`：`GatewayClient.getJson/postJson` 尚不存在（376 没有调用方）——本计划 Step 6 加。

## Architecture（契约）

```
编辑器                         扩展宿主 (Node)                             网关
─────                         ───────────────                            ────
选区/文件 ──命令──▶ promptBuilder ─┐
                                   ├─▶ sessionTarget ─▶ sessionSocket ──WS(Bearer)──▶ bind_session / start_run
中继 onRequest ──(GET …/messages)─┘        │                │  ▲
                                           │                │  └─ run_event / requires_action / run_completed
面板可见? ◀── WebviewView.visible          ▼                ▼
                                   approvals ─REST approve/deny▶   sessionState (纯 reducer)
                                                                        │
                                              ┌─────────────────────────┼──────────────────────────┐
                                              ▼                         ▼                          ▼
                                       checklistTree            statusBar                deliverables（通知 + 打开）
```

- **一个宿主 WS，长期绑定「活动会话」**：活动会话＝面板正在看的会话（中继 `onRequest` 观察到的 `observed`）；选区命令的
  目标会话若不同则改绑它。**只 open 不 bind 收不到任何 `run_event`**——镜像绑定（observed 变化即 bind）是清单树/状态栏/
  通知的承重件，见 Step 6。断线指数退避重连并用 `cursor = 上次见到的 sequence` 续上（同一会话内）；**换会话必须 cursor 归零**
  （`lastSequence` 属于旧会话）。它不是面板里 Web UI 那条 WS 的替代品——两条连接并存（见 Current state 的事实依据）。
- **纯函数内核 + 薄适配**：`sessionState.ts`（归约）、`promptBuilder.ts`、`sessionTarget.ts`、`approvalText.ts`、`deliverableGate.ts`
  全是纯函数（node:test 直接测，不需要 vscode）；`sessionSocket.ts` 只讲协议；`checklistTree.ts`／`statusBar.ts`／`approvals.ts`
  是把状态画出来的薄层。
- **原生侧只做镜像与入口，不复制对话 UI**：清单树只读（勾选状态是代理的，不让用户在树上改）；审批通知给 Approve／Deny，
  规则/前缀等细选项仍去面板；`user_interaction` 之类问题类一律「Open panel」。

## Commands you will need

前置同 376：Node ≥ 20（advisor 在 v22.22.1 验证）。全部在 `editors/vscode/` 下执行。

| Purpose | Command | Expected on success |
|---|---|---|
| 安装依赖 | `npm ci --no-audit --no-fund` | exit 0 |
| 编译 | `npm run compile` | exit 0；`dist/extension.js` 更新 |
| 类型检查 | `npm run typecheck` | exit 0，无输出（直接看退出码，别接管道） |
| 单测 | `npm test` | exit 0；`# tests 103`、`# pass 103`、`# fail 0`（376 的 54 + 本计划 49） |
| 打包 | `bash scripts/build-vscode-forebrain-ext.sh`（仓库根） | 产出 `editors/vscode/out/forebrain-vscode-0.2.0.vsix` |
| 安装到真机 | `code --install-extension editors/vscode/out/forebrain-vscode-0.2.0.vsix` | 输出含 `Successfully installed` |
| 零 harness 改动 | `git status --porcelain -- pkg frontend npm ai-elements-vue cmd scripts .gitignore` | 无输出 |

## Scope

**In scope（唯一允许修改）**
- `editors/vscode/**`（`package.json` 版本升到 `0.2.0`；新增 `src/` 模块与测试；README 增「Native integration」与键位说明）
- `plans/379-*.md`、`plans/README.md` 对应行（评审方维护）

**Out of scope**
- `pkg/**`、`frontend/**`、`ai-elements-vue/**`、`npm/**`、`cmd/**`、`scripts/**`、`.gitignore`——本计划零 harness 改动，
  打包脚本与忽略规则沿用 376。
- `editors/vscode/src/relay.ts`——中继的安全判定不许为任何便利放宽；本计划只**消费**它已有的 `onRequest`。
- 原生 Chat 参与者（`vscode.chat.createChatParticipant`）→ 380。
- 让代理通过扩展取诊断／开 diff 的 `ide` MCP 服务、「每轮自动附带选区上下文」（需 harness 侧钩子）→ 后续，见 Maintenance notes。
- 树上可勾选的清单（会与代理的 `session_todo` 状态打架）——刻意不做。

## Git workflow

- 本仓现行规则（owner 常设裁决）：**禁止提交任何代码，用户手动提交。** 执行器不 `git commit`、不 push、不 merge。
- 被派发到隔离 worktree（`.worktrees/exec-379-vscode-native`，评审方创建，基线须已含 376）；改动留未提交，报告列文件清单。

## Steps

### Step 1: 清单树与状态栏的数据内核 `src/sessionState.ts`

纯归约器，输入是宿主 WS 收到的消息（解析后的 JSON），输出一个可快照的状态对象。

```ts
export interface PlanItem { id: string; content: string; status: string; active: string }
export interface Checklist {
  /** Roster key of the agent whose list this is; 'primary' when the payload's agent_id is empty. */
  readonly agent: string;
  readonly title: string;
  readonly explanation: string;
  readonly completed: number;
  readonly total: number;
  readonly items: readonly PlanItem[];
}
export interface PendingApproval {
  readonly actionId: string;
  readonly runId: string;
  readonly kind: string;            // data.kind ?? data.action_kind ?? ''
  readonly toolName: string;        // data.permission_suggestion?.permission_tool_name ?? ''
  readonly agent: string;           // data.agent_id ?? ''
}
export interface DeliveredFile { readonly path: string; readonly title: string }
export interface SessionState {
  readonly runActive: boolean;
  readonly toolsInFlight: number;
  readonly subagents: number;
  readonly checklists: readonly Checklist[];   // primary first, then by agent key
  readonly pending: readonly PendingApproval[]; // by actionId, insertion order
  readonly lastDelivery: { readonly runId: string; readonly files: readonly DeliveredFile[] } | null;
  readonly lastRunEnd: { readonly op: 'run_completed' | 'run_cancelled' | 'run_error'; readonly error: string; readonly planDone: number; readonly planTotal: number } | null;
}
export const EMPTY_SESSION_STATE: SessionState;
/** Applies one server message (already JSON-parsed). Returns the same object when nothing changed. */
export function reduceSessionMessage(state: SessionState, msg: unknown): SessionState;
/** Removes every approval whose action_id is no longer in `pendingIds` (the REST reconcile). */
export function reconcilePending(state: SessionState, pendingIds: readonly string[]): SessionState;
/** The progress the status bar shows: null when no checklist exists. */
export function checklistProgress(state: SessionState): { done: number; total: number; active: string } | null;
```

归约规则（逐条，测试逐条对应）：

| 输入 | 效果 |
|---|---|
| `run_event` 且 `data.type === "turn_started"` | `runActive = true`，`lastRunEnd = null` |
| `run_event` 且 `data.type ∈ {turn_completed, turn_cancelled, turn_error}` | `runActive = false`，`toolsInFlight = 0`，`pending` 清空（轮次结束，挂起的审批都已作废）；**另读 `data.payload`**：`lastRunEnd = {op: 'run_' + data.type.slice(5), error: payload.error ‖ '', planDone: payload.plan_done ‖ 0, planTotal: payload.plan_total ‖ 0}`；`payload.artifacts` 非空时 `lastDelivery = {runId: data.run_id, files: [{path, title: title ‖ name}]}` |
| `run_event` 且 `data.type === "turn_failed"` | `runActive = false`，`toolsInFlight = 0` |
| `run_event` 且 `data.type === "tool_call_started"` | `toolsInFlight += 1` |
| `run_event` 且 `data.type === "tool_call_completed"` | `toolsInFlight = max(0, toolsInFlight − 1)` |
| `run_event` 且 `data.type === "subagent_spawned"` / `"subagent_ended"` | `subagents ± 1`（下限 0） |
| `run_event` 且 `data.type === "plan_updated"` | 用 `data.payload` 替换 `checklists` 里同 `agent` 的那一份（`agent = payload.agent_id ‖ 'primary'`），没有则追加；主代理永远排第一 |
| `run_event` 且 `data.type === "approval_requested"` | **观察者主路径**：同下 `requires_action` 行，字段取自 `data.payload`（`action_id, action_kind, agent_id, permission_suggestion`），`runId = data.run_id` |
| `run_event` 且 `data.type === "mode_changed"` | 忽略（面板里已有模式显示） |
| `requires_action`（发起者专属 op） | `pending` 追加 `{actionId: data.action_id ‖ data.id, runId, kind, toolName, agent}`；同 `actionId` 已存在则不重复 |
| `action_approved` / `action_denied` / `action_cancelled`（发起者专属 op） | 按 `data.action_id`（无则按 `action_id` 字段）从 `pending` 移除 |
| `run_completed` / `run_cancelled` / `run_error`（发起者专属 op） | 同 `turn_*` 行的效果，字段取自消息顶层（`error, plan_done, plan_total, artifacts`）；与同一轮先到的 `turn_*` 事件幂等（同 `runId` 的 `lastDelivery` 不再重复记） |
| `turn_withdrawn` / `turn_refused` | `runActive = false`（它们不带 run） |
| 其它（`connected`、`session_bound`、`mode_changed` 顶层 op、`pong`、`run_warning`…） | 原对象返回 |

`PendingApproval` 的取值在两条路径上统一：`toolName = permission_suggestion.permission_tool_name ‖ ''`、`agent = agent_id ‖ ''`。

健壮性：任何字段缺失都取默认值，永不抛错；`data` 不是对象时视为空。

测试 `src/sessionState.test.ts`（14 条）：逐行对应上表（`turn_started→完成`、工具计数、子代理计数、`plan_updated` 主/子代理各一份、
`run_event approval_requested` 加 pending（观察者）、`requires_action` 去重、三种回答移除、`turn_completed` 的 payload 带
`artifacts` ⇒ `lastDelivery`（观察者）、三种发起者结束 op 的 `lastRunEnd` 与 `lastDelivery`、轮次结束清空 `pending`、
`turn_withdrawn`、未知消息原对象、`reconcilePending`、`checklistProgress` 取主代理清单）。

**Verify**：`npm test` → `# tests 68`（54+14）、`# fail 0`；`npm run typecheck` exit 0。

### Step 2: 宿主 WS `src/sessionSocket.ts`

```ts
export interface ServerMessage { op: string; [k: string]: unknown }
export type Listener = (msg: ServerMessage) => void;

export interface SessionSocketOptions {
  readonly origin: string;          // the gateway's http(s) origin
  readonly getToken: () => string;  // read on every (re)connect: the token may have been re-entered
  readonly log: (line: string) => void;
  readonly listener: Listener;
  /** Link transitions for the status bar: true when the `connected` frame arrives, false when the socket closes/errors. */
  readonly onLink?: (up: boolean) => void;
}

export class SessionSocket {
  /** Resolves once the server's `connected` frame has arrived. Rejects if called after `close()`. */
  open(): Promise<void>;
  /** Binds to a session (re-binding another session is allowed) and resolves on `session_bound` — NOT on the end of the history replay, which the server sends after it (server.go:1505-1527). */
  bind(sessionId: string, cursor = 0): Promise<void>;
  /** Sends start_run and resolves with run_id on run_started; rejects on turn_withdrawn / turn_refused / run_error for that request_id, or after 15s. */
  startRun(sessionId: string, content: string): Promise<string>;
  /** Stops for good: no reconnect and no further `open()` on this instance. Idempotent. */
  close(): Promise<void>;
  readonly boundSession: string | undefined;
  readonly lastSequence: number;    // highest data.sequence seen on a run_event, for bind(cursor)
}
```

生命周期：一个 `SessionSocket` 实例对应一段「已连接」时期——`Connection` 进入 ready 态时新建并 `open()`，进入 unavailable 态或
扩展停用时 `close()`；再次 ready 就再新建一个。**没有** `decide()`：Approve／Deny 走 REST（Step 5），因为审批的回执
`action_approved` 只回到发起 `approve_action` 的那条连接，走 WS 也拿不到确认，还要多一套等待逻辑。
绑定目标由 extension.ts 决定（Step 6 的镜像绑定：observed 变化即 `bind(seen, 0)`）；本类只负责同一实例内断线后按
`boundSession`/`lastSequence` 自动续绑。`bind`/`startRun` 在连接未就绪（open 未完成或重连中）时立即 reject。
`boundSession` 在 `bind` 发起时即更新（不等 `session_bound`），供镜像绑定去重。

实现要点（逐条）：
- `new WebSocket(wsUrl, { headers: { authorization: `Bearer ${token}` }, handshakeTimeout: 10_000 })`；**不设 `origin`**。
- 入站统一 `JSON.parse`（失败则丢弃并 `log` 一行）；**先喂给 `listener`**（归约器在那一层），再处理本类自己的等待项
  （按 `request_id` 配对的 `session_bound` / `run_started` / `turn_*` / `run_error`）。
- 出站帧的字段名**逐字**取自 `wsClientMsg` 的 json tag，且都带 `protocol_version: "2026-04-18"` 与 `request_id`：
  `{op:"bind_session", protocol_version, request_id, session_id, cursor}`、
  `{op:"start_run", protocol_version, request_id, session_id, message:{content, role:"user"}}`。本计划不发别的 op。
- 重连：`close`/`error` 事件后按 0.5s→1s→2s→4s→8s（封顶 8s）退避重连；每次重连成功后若 `boundSession` 非空则
  `bind(boundSession, lastSequence)`；显式 `close()` 后不再重连。重连期间 `startRun` 直接 reject（提示面板里发）。
- 等待项超时：`bind` 15s、`startRun` 15s；收到 `run_warning` 且 `message === "session_event_snapshot_unavailable"` 时 `bind` reject；
  **socket 关闭时所有未完成的等待项立即 reject**（不要空等 15s 超时）。
- 服务端还可能在回放期间因快照尾缓冲溢出而发 `run_warning` 且 `message === "session_event_backpressure_resync_required"`，
  随后主动关闭连接（`server.go:1810-1820`，CloseTryAgainLater “reconnect with cursor”）：无需特殊处理——close 事件走上面的
  重连路径，`bind(boundSession, lastSequence)` 从 durable 日志续上（事件不丢，溢出的只是内存尾缓冲）。
- `onLink` 回调：`open()` resolve（`connected` 帧到达）后调 `onLink(true)`；`close`/`error` 时（退避之前）调 `onLink(false)`。
  状态栏的「未连接」态靠它——网关进程死掉时 `Connection` 仍是 ready（376 没有健康轮询），只有 socket 知道链路断了。
- 服务端每 20s ping 一次、75s 无数据断开；`ws` 自动回 pong，本类不额外发心跳。

测试 `src/sessionSocket.test.ts`（9 条，用 `src/testing/mockChatServer.ts`：`ws.WebSocketServer({ noServer: true })` + `http.createServer`，
握手校验 Bearer（不对则 destroy）；**两种角色**——`observer`（只发 `connected`，对 `bind_session` 回 `session_bound` 再按脚本推
`run_event`，**从不**发 `requires_action`/`run_completed`，与真实网关对观察者连接的行为一致）与 `initiator`（对 `start_run` 回
`run_started` 再按脚本推发起者专属 op）；可强制断开连接）：

| # | 用例 | 断言 |
|---|---|---|
| 1 | 握手与首帧 | 服务端收到 `authorization: Bearer …` 且**没有** origin；`open()` 在收到 `connected` 后才 resolve；`onLink` 收到 `true` |
| 2 | `bind` 帧形状 | 服务端收到的 JSON 恰为 `{op:"bind_session", protocol_version:"2026-04-18", request_id:<string>, session_id:"s1", cursor:0}`（键集合完全相等） |
| 3 | 历史回放进 listener（无竞态写法） | `bind()` resolve 后，**在 2s 内等到** listener 收到那条 `run_event`（用一个由 listener 兑现的 Promise），再断言 `lastSequence` 等于其 `data.sequence`。不得断言「bind resolve 的那一刻已收到」——服务端先发 `session_bound` 后回放，实测 300 轮里约 6% 不成立 |
| 4 | `startRun` 帧形状与结果 | 帧的 `message` 恰为 `{content, role:"user"}`；resolve 值等于服务端给的 `run_id` |
| 5 | `turn_withdrawn` 配对拒绝 | `startRun` reject，消息含服务端 `error` 文本 |
| 6 | 观察者收得到审批事件 | `observer` 角色推 `run_event{type:"approval_requested", payload:{action_id:"a9", …}}` → listener 收到；全程没有 `requires_action` op |
| 7 | 重连续上 | 服务端强制断开后 ≤ 1.5s 内自动重连并重新收到 `bind_session` 且 `cursor` 等于断开前的 `lastSequence`；`onLink` 先 `false` 后 `true` |
| 8 | `close()` | 之后不再重连；二次 `close()` 不抛；`close()` 后再 `open()` reject |
| 9 | 服务端先关（75s 读超时的替身：直接 destroy） | 与第 7 条同一机制，断言退避后至多 8.5s 内恢复；`onLink` `false`→`true` |

（上表 9 条；累计 77。）

**Verify**：`npm test` → `# tests 77`、`# fail 0`；`npm run typecheck` exit 0。

### Step 3: 纯函数四件套 `src/promptBuilder.ts`、`src/sessionTarget.ts`、`src/approvalText.ts`、`src/deliverableGate.ts`

四个文件都**不 import `vscode`**（376 Step 3 的规则：要单测的模块不碰 `vscode`；类型用结构子集）。

`promptBuilder.ts`：

```ts
export interface SelectionContext { readonly fsPath: string; readonly relPath: string; readonly languageId: string;
  readonly startLine: number; readonly endLine: number; readonly selection: string }   // lines are 1-based
export interface FileContext { readonly fsPath: string; readonly relPath: string; readonly languageId: string; readonly content: string }
export interface DiagnosticLine { readonly line: number; readonly severity: string; readonly source: string; readonly message: string }
/** The shape of vscode.Diagnostic this module reads. `severity` is the numeric DiagnosticSeverity: 0 Error, 1 Warning, 2 Information, 3 Hint. */
export interface DiagnosticLike { readonly range: { readonly start: { readonly line: number } }; readonly severity: number;
  readonly source?: string | undefined; readonly message: string }
export const FILE_CONTEXT_LIMIT = 64 * 1024;
export function buildSelectionPrompt(sel: SelectionContext, question: string, diagnostics: readonly DiagnosticLine[]): string;
export function buildFilePrompt(file: FileContext, question: string, diagnostics: readonly DiagnosticLine[]): string;
/** 0→'ERROR', 1→'WARNING', 2→'INFO', 3→'HINT'; `line` is the 0-based range start made 1-based; `source` defaults to ''. */
export function diagnosticsOf(entries: readonly DiagnosticLike[]): DiagnosticLine[];
```

形状（选区）：第一行 `In <relPath>:<start>-<end>:`，空行，` ```<languageId> ` 围栏代码，空行，诊断块（有才有）
`Diagnostics reported in <relPath>:` + 每条 `- [ERROR] line 12: <message> (<source>)`，空行，最后是用户问题。文件版：首行
`File <relPath>:`；内容超过 `FILE_CONTEXT_LIMIT` 时截到 64KB 并在围栏后加一行
`(<relPath> truncated at 64 KiB; use your file tools to read the rest)`。诊断按行号排序、同一行按 ERROR 先。
`relPath` 为空（没有工作区）时用 `fsPath`（绝对路径）。

`sessionTarget.ts`：

```ts
export type SelectionTarget = 'current' | 'always-new';
export interface TargetInputs { readonly setting: SelectionTarget; readonly explicit: string | undefined;
  readonly observed: string | undefined; readonly recent: string | undefined }
/** Which existing session a command targets, or null = create a new one. */
export function resolveTargetSession(input: TargetInputs): string | null;
/** The session the Web UI is reading: `GET /api/chat/sessions/<id>/messages` (with or without a query string). */
export function sessionFromRequestPath(method: string, path: string): string | undefined;
/** The project row whose root is the folder (both through path.resolve; no symlink chase). */
export function projectForFolder(projects: readonly { readonly id: string; readonly root: string }[], folder: string): { id: string; root: string } | undefined;
/** The newest conversation of a project: the list endpoint answers newest-first. */
export function newestSession(records: readonly { readonly id: string }[]): string | undefined;
/** Title of the conversation a command creates. */
export function sessionTitle(fileBasename: string): string;   // `VS Code: <basename>`
```

顺序即契约（owner 2026-11-24 裁决「默认进面板当前显示的会话」）：`setting === 'always-new'` → `null`；否则
**`observed ?? explicit ?? recent ?? null`**。`observed`＝中继最近一次看到的 `GET /api/chat/sessions/<id>/messages`（面板此刻在看的
会话；SPA 的实际请求带查询串，如 `…/messages?limit=0`，`frontend/src/lib/api.ts:3058`），正则
`/^\/api\/chat\/sessions\/([A-Za-z0-9_-]{1,128})\/messages(?:\?|$)/` 且只认 `GET`；`explicit`＝扩展自己上次发送的目标
（`workspaceState['forebrain.lastTargetSession']`，只在面板当前会话未知时兜底）；`recent`＝工作区文件夹对应项目里最新一条会话
（`GET /api/v1/projects` 用 `projectForFolder` 按 `root` 匹配 → `GET /api/chat/sessions?project_id=<id>` 的 `records[0]`）。

`approvalText.ts`：

```ts
/** One line for the notification, from a requires_action's data. */
export function approvalHeadline(p: PendingApproval): string;   // "Forebrain: allow <toolName>?" when toolName, else "Forebrain needs your input (<kind>)"
/** The second line: the command/input when it is a string, otherwise nothing. */
export function approvalDetail(permissionInput: unknown): string;  // string → first 200 chars; {command} → that; else ''
```

`deliverableGate.ts`：

```ts
/** Paths a command may open in the editor: absolute, and inside one of the given workspace roots (lexically, after normalisation). */
export function openableDeliverables(files: readonly DeliveredFile[], workspaceRoots: readonly string[]): DeliveredFile[];
```

规则：`path.isAbsolute` 且规范化后以某个 `root + path.sep` 开头（或等于 root）才可开；其余只列不进编辑器。用 `path.resolve`
规范化，不做 `..` 逃逸的特判以外的事。

测试：`promptBuilder.test.ts` 6 条（选区形状含行号与围栏语言、文件形状、超 64KB 截断注记、诊断排序与 0/1/2/3→ERROR/WARNING/INFO/HINT
映射且行号 +1、无诊断时无诊断块、`relPath` 为空时回退 `fsPath`）；`sessionTarget.test.ts` 7 条（`always-new`；`observed` 胜过
`explicit` 与 `recent`；无 `observed` 时 `explicit` 胜过 `recent`；只剩 `recent`；全空 → `null`；`sessionFromRequestPath` 对
`…/messages?limit=0` 取出 id、对非 GET／`…/events`／畸形 id 返回 undefined；`projectForFolder`／`newestSession`／`sessionTitle`）；
`approvalText.test.ts` 3 条；`deliverableGate.test.ts` 4 条（工作区内、`..` 逃逸、相对路径、无工作区根）。

**Verify**：`npm test` → `# tests 97`、`# fail 0`；`npm run typecheck` exit 0。
（77 + 6 + 7 + 3 + 4 = 97。）

### Step 4: 把 376 的接缝打开——面板会话深链、中继观察、`showSession`

改 `src/panelHtml.ts`（纯函数，376 的 `panelUrl` 已经支持第三个参数）：`PanelState` 的 ready 分支增加可选 `session?: string`；
`renderPanelHtml` 在 ready 时调 `panelUrl(state.relayOrigin, state.boot, state.session)`——URL 成为
`<relayOrigin>/?boot=<boot>&session=<id>`；`session` 不匹配 `/^[A-Za-z0-9_-]{1,128}$/` 时 `panelUrl` 抛错，所以调用前先丢弃
非法值（`SESSION_ID.test(session) ? session : undefined`），**不要让一个坏 id 把整个面板渲染打挂**。
`panelHtml.test.ts` 增 2 条：带合法 `session` 时 src 含 `?boot=<BOOT>&session=<id>`；带 `../x` 时渲染成功且 URL 不含 `session=`。

改 `src/connection.ts`：
- `ConnectionDeps` 增加 `onRequest?: (method: string, path: string) => void`，`connectOnce` 里把
  `onRequest` 与原有日志合并成一个回调传给 `startRelay`（先日志、后 `onRequest`；两者都只拿到 method 与 path——日志那一路
  仍按 376 剥掉查询串，`onRequest` 这一路拿到完整 path，因为会话 id 在路径里而不是查询串里）。
- 新增 `showSession(sessionId: string)`：ready 态下重新 `emit({kind:'ready', relayOrigin: this.relay.origin, boot, session: sessionId})`
  （`boot` 是 `connectOnce` 里存下的那份，作为字段保存）；非 ready 态 reject。
  读 token 用 376 已有的 `currentToken()`，不改名、不另加 `token()`。
- `connection.test.ts` 增 2 条：`onRequest` 能收到经过中继的请求 method 与完整 path（含查询串）；`showSession` 在 ready 态 emit
  带 `session` 且 `boot` 不变的状态、非 ready 态 reject。

**Verify**：`npm test` → `# tests 101`、`# fail 0`；`npm run typecheck` exit 0。

### Step 5: 原生表面 `src/checklistTree.ts`、`src/statusBar.ts`、`src/approvals.ts`、`src/deliverables.ts`

`checklistTree.ts`：
- `class ChecklistTree implements vscode.TreeDataProvider<ChecklistRow>`；根行＝每份 `Checklist`（主代理标题为 payload `title`，
  子代理标题为 `Subagent: <agent>`），子行＝`PlanItem`；图标：`completed`→`$(check-all)`、`in_progress`→`$(sync~spin)`、
  `cancelled`→`$(x)`、其余→`$(circle-large)`；`description`＝`item.active ‖ ''`；`tooltip`＝`content`。
- `update(state: SessionState)`：结构变化才 `_onDidChangeTreeData.fire(undefined)`（比较 JSON 串）。
- 无清单时 `vscode.commands.executeCommand('setContext', 'forebrain.checklistEmpty', true)` 让 `viewsWelcome` 显示——**激活时先调一次
  `update(EMPTY_SESSION_STATE)`**，否则第一次状态变化之前空态说明不出现。
- 树行不可点击跳转（卡片在面板里）；命令 `Forebrain: Open Chat Panel`（376 的 `forebrain.openPanel`）仍是入口。

`statusBar.ts`：
- `createStatusBarItem('forebrain', vscode.StatusBarAlignment.Left, 100)`；`update(state, connected)`（`connected`＝宿主 socket
  链路状态 `linkUp`，见 Step 6）：**`connected === false` 最优先** → `` `$(debug-disconnect) Forebrain` ``（网关死掉时 state 里
  可能还留着 running/清单，未连接态必须先显示）；否则不活跃 → `` `$(radio-tower) Forebrain` ``；运行中 →
  `` `$(sync~spin) Forebrain` ``；有清单 → 在基座文本后追加 `` `$(check-all) ${done}/${total}` ``；`pending.length > 0` → 追加 `` `$(bell) ${n}` ``。
  tooltip 写明会话 id 与各计数；点击 `forebrain.chat.focus`。

`approvals.ts`：
- `class Approvals`：构造依赖 `{ client: GatewayClient, token: () => string, boundSession: () => string | undefined,
  notificationsEnabled: () => boolean, focusPanel: () => void }`；`dispose()` 清调和定时器。
  `onState(prev, next, chatVisible)`——`next.pending` 里新增的条目：若设置 `forebrain.approvalNotifications`
  为 true 且 `!chatVisible` → `approvalHeadline` + `approvalDetail` 弹通知：`toolName` 非空 → `showWarningMessage(text, 'Approve', 'Deny', 'Open panel')`，
  否则 `showInformationMessage(text, 'Open panel')`。**Approve／Deny 走 REST**（`client.postJson('/api/actions/<actionId>/approve', token,
  {decision:'accept'})`／`…/deny`；与下面的调和端点同一条通道），失败则 `showErrorMessage`；Open panel → focus。
  `chatVisible` 为 true 时**不弹**（面板里已有卡，避免双弹）。
- 调和：`pending` 非空时每 4s `GET /api/actions?status=pending&session_id=<bound>`（`getJson`），用 `reconcilePending` 清掉已答的
  ——这是观察者连接得知「审批在面板里被答掉了」的唯一途径（没有 `approval_resolved` 事件）；归零即停。定时器随 `dispose()` 清掉，绝不泄漏。

`deliverables.ts`：
- `onRunEnd(prev, next)`：`next.lastDelivery` 与 `prev` 不同且非空 → `showInformationMessage(`Forebrain delivered ${n} file(s)`, 'Open…', 'Reveal')`；
  'Open…' → QuickPick（标题/路径）→ `openDelivered(path)`；'Reveal' → `vscode.commands.executeCommand('revealInExplorer', uri)`
  （仅工作区内）。
- `openDelivered(path)`：`vscode.workspace.getWorkspaceFolder(vscode.Uri.file(path))` 为真 → `vscode.window.showTextDocument(uri, {preview:false})`；
  否则 `showInformationMessage`「该交付物在工作区之外：<path>」。
- 命令 `forebrain.openDeliveredFile`：拉 `GET /api/chat/sessions/<bound>/artifacts?limit=50`，QuickPick 后同上。

测试：`checklistTree`/`statusBar`/`approvals`/`deliverables` 是 vscode 薄层，不为它们写需要 mock `vscode` 的测试；
它们读写的每个决策都已由 Step 1–4 的纯函数测试钉住（图标映射除外——那是展示）。但 `deliverableGate` 与 `approvalText`
的用例已覆盖其判定。本步只保证类型检查与手工验收。

**Verify**：`npm run compile` exit 0；`npm run typecheck` exit 0；`npm test` 仍为 `# tests 101`、`# fail 0`。

### Step 6: 命令与键位 `src/commands.ts` + manifest 增量

`package.json`：`version` 升为 `0.2.0`；`ws` 从 `devDependencies` 移到 `dependencies`（宿主 WS 是运行时依赖，379 起才成立）；
`contributes` 只增不删——`views.forebrain` 追加清单视图，`commands` 追加三条，
新增 `viewsWelcome`、`keybindings`、`menus`，`configuration.properties` 追加三个设置：

```json
"views": {
  "forebrain": [
    { "id": "forebrain.chat", "name": "Chat", "type": "webview", "contextualTitle": "Forebrain" },
    { "id": "forebrain.checklist", "name": "Checklist", "contextualTitle": "Forebrain" }
  ]
},
"viewsWelcome": [
  { "view": "forebrain.checklist", "contents": "No checklist yet. It fills in as the agent calls session_todo.", "when": "forebrain.checklistEmpty" }
],
"commands": [
  { "command": "forebrain.askAboutSelection", "title": "Forebrain: Ask About Selection", "icon": "$(comment-discussion)" },
  { "command": "forebrain.askAboutFile", "title": "Forebrain: Ask About File", "icon": "$(comment-discussion)" },
  { "command": "forebrain.openDeliveredFile", "title": "Forebrain: Open Delivered File" }
],
"keybindings": [
  { "command": "forebrain.askAboutSelection", "key": "ctrl+alt+k", "mac": "ctrl+cmd+k", "when": "editorHasSelection" }
],
"menus": {
  "editor/context": [
    { "when": "editorHasSelection", "command": "forebrain.askAboutSelection", "group": "9_forebrain" },
    { "when": "!editorHasSelection", "command": "forebrain.askAboutFile", "group": "9_forebrain" }
  ],
  "explorer/context": [
    { "when": "resourceScheme == file && !explorerResourceIsFolder", "command": "forebrain.askAboutFile", "group": "9_forebrain" }
  ],
  "view/title": [
    { "when": "view == forebrain.chat", "command": "forebrain.openWebUiInBrowser", "group": "navigation@1" }
  ]
}
```

设置增量（`configuration.properties`）：

```json
"forebrain.selectionTarget": {
  "type": "string", "enum": ["current", "always-new"], "default": "current",
  "description": "Where Ask About Selection/File sends: the session the panel is showing, or always a new one."
},
"forebrain.includeDiagnostics": {
  "type": "boolean", "default": true,
  "description": "Append the file's current diagnostics to the prompt of Ask About Selection/File."
},
"forebrain.approvalNotifications": {
  "type": "boolean", "default": true,
  "description": "Show a VS Code notification for an approval that arrives while the chat panel is not visible."
}
```

`forebrain.openWebUiInBrowser` 出现在 `view/title` 菜单里要有图标：给 376 的那条 command 补 `"icon": "$(link-external)"`。

`src/commands.ts`（vscode 薄层；判定都在 Step 3 的纯函数里）：

```ts
export class ForebrainCommands {
  constructor(private readonly deps: {
    readonly workspaceState: vscode.Memento;                 // forebrain.lastTargetSession
    readonly connected: () => boolean;                       // 实现：Boolean(active?.currentToken())；token 在 relay 起来前置位的瞬态由 showSession/startTurn 的 ready 守卫兜底
    readonly token: () => string;
    readonly client: () => Pick<GatewayClient, 'getJson' | 'postJson'>;  // 实现：() => new GatewayClient(active!.origin)——纯 fetch 包装，按需构造；connected() 已先守卫
    readonly observedSession: () => string | undefined;      // what the relay last saw the Web UI read
    readonly showSession: (id: string) => Promise<void>;
    /** Binds the host socket to the session when needed, then start_run; resolves with run_id. */
    readonly startTurn: (sessionId: string, content: string) => Promise<string>;
    readonly focusPanel: () => void;
  }) {}

  /** "Ask About Selection": the question is required; Esc cancels and creates nothing. */
  async askAboutSelection(): Promise<void> { … }
  /** "Ask About File": whole file as context. */
  async askAboutFile(uri?: vscode.Uri): Promise<void> { … }
}
```

流程（两个命令共用 `send(context, question)`）：
1. **未连接**（`!connected()`）→ `showWarningMessage('Forebrain is not connected. Open the Forebrain panel and connect first.')`，结束——
   不建会话、不改面板。
2. 没有活动编辑器/资源 → `showWarningMessage('Open a file first.')`，结束。
3. `showInputBox({ prompt: 'Ask Forebrain about the selection', placeHolder: 'e.g. There is a bug here — fix it', ignoreFocusOut: true })`；
   `undefined`（Esc）→ 直接返回，**不建会话、不改面板**。
4. 组 prompt（Step 3）；设置 `forebrain.includeDiagnostics`（默认 true）时附上该文件当前诊断。
5. 目标会话：先算 `recent`——`GET /api/v1/projects`（`getJson`）→ `projectForFolder(projects, folder)` → 命中时
   `GET /api/chat/sessions?project_id=<id>` → `newestSession(records)`；再
   `resolveTargetSession({ setting, explicit: workspaceState.get('forebrain.lastTargetSession'), observed: observedSession(), recent })`；
   结果为 `null` → `POST /api/chat/sessions {title: sessionTitle(basename), folder}`（**不带 `source`**）取 `id`。
   `folder`＝资源所在的工作区文件夹（`getWorkspaceFolder(uri)?.uri.fsPath`；多根取资源所在的那个；没有文件夹 → 警告后结束）。
6. 仅当 `target !== observedSession()` 时调 `showSession(target)` 让面板显示该会话——目标就是面板当前显示的会话时
   **跳过 `showSession`**：重新 emit ready 会重建 iframe、Web UI 重载并断掉正在流的回复（验收第 3 条要求「面板自身不重连、
   不断流」）；新一轮会经面板自己的 WS 到达面板。然后 `startTurn(target, prompt)`。
7. 成功：`workspaceState.update('forebrain.lastTargetSession', target)`；`focusPanel()`。任一步 reject →
   `showErrorMessage(\`Forebrain did not start the turn: ${message}\`)`。例外：`turn_withdrawn` 且 `data.code` 为四个会话忙
   code 之一（`session_running`／`session_awaiting_approval`／`session_running_elsewhere`／`session_awaiting_approval_elsewhere`）
   → 这是预期的忙拒绝，直接显示服务端 `error` 文本（已是说人话的英文句子，如 “This conversation is already running a turn;
   send again when it finishes.”），不重试、不算失败。

`GatewayClient` 在本步扩两个方法与一个错误类（376 没有调用方所以没有；从此有了）：

```ts
/** Nothing answered, or nothing answered in time. */
export class GatewayUnreachableError extends Error {}
/** GET with the token; no answer is a GatewayUnreachableError, any non-2xx answer a GatewayHttpError. */
async getJson<T>(path: string, token: string): Promise<T>;
/** POST a JSON body with the token; same errors as getJson. */
async postJson<T>(path: string, token: string, body: unknown): Promise<T>;
```

两者与 376 的 `get` 同构：token 逐次传入（不持有可变状态）、带 `Authorization: Bearer`、沿用 `REQUEST_TIMEOUT_MS`（5 秒请求超时）；
`get` 现在把「无应答」作为 `'unreachable'` 返回，`probe` 仍把它映射成三态，`getJson`/`postJson` 把它抛成 `GatewayUnreachableError`。
`gateway.test.ts` 增 2 条：`getJson` 发出 `authorization: Bearer …` 且 401 抛 `GatewayHttpError`（`status === 401`）；`postJson` 发出
`content-type: application/json` 与 JSON 正文。

**注册时机与可见性**（不是流程的一步）：
- `activate` 里在 376 的 `boot()` **之前**创建 `ChecklistTree`（`vscode.window.createTreeView('forebrain.checklist', …)`）、`StatusBar`、
  `Approvals`、`Deliverables` 与 `ForebrainCommands`，全部放进 `context.subscriptions`；创建后立刻 `checklist.update(EMPTY_SESSION_STATE)`
  （否则空态说明在第一个事件到来前不显示）。
- **宿主 socket 的生命周期跟随 `Connection` 的状态**，不跟随命令：376 的 `build()` 里 `emit` 改为
  `(state) => { panel.setState(state); onConnectionState(state); }`；`onConnectionState` 在 `ready` 且尚无 socket 时新建
  `SessionSocket({ origin: active.origin, … })` 并 `open()`，在 `connecting`/`unavailable` 时 `close()` 并丢弃实例（下次 ready 再新建——
  `SessionSocket` 实例不可复用，Step 2）。配置来源只用 `active.origin`，不再调 `readGatewayConfig()`（它在 376 里已被
  `boot()` 的 try/catch 兜住，设置非法时 `active` 为 undefined，这里就什么都不建）。
- `observed` 的来源与**镜像绑定**（承重件：宿主 WS 只有 bind 了会话才收得到 `run_event`——只 open 不 bind，清单树/状态栏/
  通知全死，验收 1/2/7/8/9 全部不可达）：376 的 `build()` 里给 `ConnectionDeps.onRequest` 传
  `(method, path) => { const seen = sessionFromRequestPath(method, path); if (seen && seen !== observed) { observed = seen; state.current = EMPTY_SESSION_STATE; checklist.update(state.current); statusBar.update(state.current, linkUp); if (socket && socket.boundSession !== seen) void socket.bind(seen, 0); } }`。
  换会话先清空归约状态（旧会话的清单/审批不属于新会话），再 `bind(seen, 0)`——**cursor 必须归零**，`lastSequence` 是旧会话的。
  socket 未 open 完成时 bind 会 reject（Step 2），`void` 掉即可——open 完成后的兜底见下条。
- **open 完成与重连后的兜底绑定**：`socket.open()` resolve 后若 `observed` 非空且 `socket.boundSession !== observed`，
  `bind(observed, 0)`；Connection 重连产生新 socket 实例时同样走这条（新实例 `boundSession` 为空，Step 2 的自动重 bind 只覆盖
  同一实例内的断线）。
- **socket 的清理必须显式接线**（`Connection.dispose()` 不 emit 状态，`onConnectionState` 等不到）：① `deactivate` 与
  subscriptions 的 dispose 里在 `active?.dispose()` 之外显式 `await socket?.close()`（并置 `linkUp = false`）；
  ② 把 `(state) => { panel.setState(state); onConnectionState(state); }` 提成 `setPanelState` helper，376 `build()` 的 `emit`
  与 `boot()` 的 catch 都用它——否则设置改成非法 URL 时 `active` 留 undefined、旧 socket 带着空 token 无限退避重连。
- `ChatPanel` 暴露只读 `visible`（即 `WebviewView.visible`）。`approvals.onState(prev, next, visible)` 在每次
  归约后调用：只在 `!visible` 时为**新增**的审批弹通知——面板可见时审批卡已经在眼前，不双弹。（不暴露
  `onDidChangeVisibility`：没有消费方，死代码不留。）

`extension.ts` 的增量：

```ts
const state = { current: EMPTY_SESSION_STATE };
let socket: SessionSocket | undefined;
let observed: string | undefined;
let linkUp = false;   // 宿主 socket 的链路状态：状态栏「已连接／未连接」读它，不读 currentToken()
const reflect = (prev: SessionState, next: SessionState): void => {
  checklist.update(next);
  statusBar.update(next, linkUp);
  approvals.onState(prev, next, panel.visible);
  deliverables.onRunEnd(prev, next);
};
const listener = (msg: ServerMessage): void => {
  const prev = state.current;
  state.current = reduceSessionMessage(state.current, msg);
  if (state.current !== prev) reflect(prev, state.current);
};
const onConnectionState = (s: PanelState): void => {
  if (s.kind === 'ready' && !socket && active) {
    socket = new SessionSocket({
      origin: active.origin, getToken: () => active?.currentToken() ?? '', log, listener,
      onLink: (up) => { linkUp = up; statusBar.update(state.current, up); },
    });
    void socket.open();
  } else if (s.kind !== 'ready' && socket) {
    void socket.close();
    socket = undefined;
    linkUp = false;
    state.current = EMPTY_SESSION_STATE;   // the next connection rebuilds it from the rebind's replay
    checklist.update(state.current);
    statusBar.update(state.current, false);
  }
};
checklist.update(EMPTY_SESSION_STATE);
```

`Deliverables.onRunEnd(prev, next)` 比较前后 `lastDelivery`，只在出现新的非空交付时通知一次。重连后的历史回放只带 `run_event`，
其中会再有 `turn_completed`（payload 带 `artifacts`）——所以 `onRunEnd` 以 **`runId` 去重**：同一个 `runId` 的交付只通知一次
（在 `Deliverables` 里记最近通知过的 `runId`）。

**Verify**：`npm test` → `# tests 103`、`# pass 103`、`# fail 0`；`npm run compile` 与 `npm run typecheck` 均 exit 0。

### Step 7: README 增量、打包与安装

`editors/vscode/README.md` 增「Native integration」一节，逐项写清：
- Checklist 视图：随代理调用 `session_todo` 实时更新，按代理分组（子代理各一节）；只读，状态由代理维护。
- 状态栏各态的含义（`$(radio-tower)` 已连接、`$(sync~spin)` 运行中、`$(check-all) 3/7` 进度、`$(bell) n` 待审批、`$(debug-disconnect)` 未连接）。
- `Forebrain: Ask About Selection`（`Ctrl+Cmd+K`／Win/Linux `Ctrl+Alt+K`，需有选区）与 `Forebrain: Ask About File`（编辑器／资源管理器右键）：需要面板已连接；
  默认送进**面板当前显示的会话**；面板还没打开任何会话时用上次发送过的会话，再退到本项目最近会话，都没有才新建；
  `forebrain.selectionTarget: "always-new"` 改为总是新建。
- 审批通知：只在面板不可见时弹；Approve／Deny 走 REST，细选项（规则、前缀）去面板；问题类输入（`user_interaction` 等）一律「Open panel」。
- `Forebrain: Open Delivered File`：只打开工作区内的路径。
- 三个新设置的表格（Step 6）。
- 已知限制追加一条：清单树是镜像，不提供勾选。

打包与安装（脚本与忽略规则沿用 376；脚本从 `package.json` 读版本号，本计划把版本升到 `0.2.0`）：

```bash
bash scripts/build-vscode-forebrain-ext.sh
code --install-extension editors/vscode/out/forebrain-vscode-0.2.0.vsix
code --list-extensions --show-versions | grep forebrain.forebrain-vscode
```

**Verify**：第一条末行 `built …/forebrain-vscode-0.2.0.vsix`；`unzip -l editors/vscode/out/forebrain-vscode-0.2.0.vsix | grep -c "extension/node_modules/ws/package.json"`
→ `1`（`ws` 自本计划起是运行时依赖）；第二条输出含 `Successfully installed`；第三条打印 `forebrain.forebrain-vscode@0.2.0`。

### Step 8: owner 验收清单（执行器看不到 UI；逐条勾选，截图放 `editors/vscode/out/acceptance/`，该目录已 gitignore）

前置：376 的验收 1–7 已通过（面板里 Web UI 可用）。每条都写明预期，不满足即按 STOP conditions 处理。

1. **清单树**：在面板里让代理用 `session_todo` 写入 ≥4 条待办并逐条推进 → `Forebrain › Checklist` 视图出现一节，条目图标随
   `pending → in_progress → completed` 变化，状态栏出现 `$(check-all) 1/4` 并随进度更新。
2. **子代理清单**：让主代理派一个会调用 `session_todo` 的子代理 → 清单树多出一节「Subagent: <agent>」，主代理那节保持第一。
3. **选区 → 当前会话**（这一条同时让面板的 WS 与宿主 WS 同绑一个会话，是「双连接并存」的验收）：选中一段代码按 `Ctrl+Cmd+K` →
   输入问题 → 面板被聚焦，**当前显示的会话**里出现这一轮（用户消息含 `In <相对路径>:<起>-<止>:` 与围栏代码），回复开始流式，
   面板自身不重连、不断流。按 Esc 取消输入框 → 不新建会话、面板不变。未连接时（面板停在未连接态）→ 只看到提示，不建会话。
4. **面板切到别的会话后再问**：先在面板里切到另一个会话，再做第 3 条 → 这一轮进的是**切换后的当前会话**（`observed` 优先），
   面板不跳回旧会话；新开一个还没有任何会话的窗口直接做第 3 条 → 落到本项目最近会话（`recent`），没有历史则新建。
5. **总是新会话**：设置 `forebrain.selectionTarget` 为 `always-new` → 重载窗口 → 再做第 3 条 → 新会话被创建并出现在该项目会话列表。
6. **文件入口**：资源管理器里右键一个文件 → `Forebrain: Ask About File` → 同第 3 条（prompt 首行为 `File <相对路径>:`）。
7. **原生审批（观察者路径）**：在面板里发起一轮会触发 shell 审批的请求 → 立刻把面板切到别的视图（不可见）→ VS Code 通知
   `Forebrain: allow shell?` 出现 → 点 Approve（走 REST）→ 轮次继续、面板里审批卡同步消失；保持面板可见再做一次 → **不弹**通知
   （卡在面板里）。面板不可见时审批在面板里或另一表面（终端／另一窗口）被答掉 → ≤ 4s 内状态栏的 `$(bell)` 计数消失（REST 调和）。
8. **交付物**：让一轮交付一个工作区内文件 → 通知「Forebrain delivered 1 file(s)」→ Open… → 在编辑器打开；工作区外路径只提示不打开。
9. **重连**：杀掉网关进程——扩展自起的：Output 里有 pid，`kill <pid>`；终端里起的：在该终端 Ctrl+C
   （`forebrain gateway stop` 的服务端 handler 并不存在，别用它）→ 进程退出即断开
   socket，`onLink(false)` 驱动状态栏 1–2s 内变未连接；重新 `forebrain gateway start` 并 `Forebrain: Enter Gateway Token` → 面板恢复后 ≤ 8s 内状态栏恢复、
   清单树经 `bind` 回放重建（中继重建，面板自动重载）。
10. **键位冲突**：`File › Preferences › Keyboard Shortcuts` 搜 `forebrain.askAboutSelection` → 无冲突标记。

## Test plan

- 全部 `node:test` + `node:assert/strict`，376 的 54 条保持全绿；本计划新增 49 条：
  `sessionState.test.ts` 14、`sessionSocket.test.ts` 9、`promptBuilder.test.ts` 6、`sessionTarget.test.ts` 7、
  `approvalText.test.ts` 3、`deliverableGate.test.ts` 4、`panelHtml.test.ts` +2、`connection.test.ts` +2、`gateway.test.ts` +2 ⇒ **共 103**。
- `sessionSocket` 的测试替身 `src/testing/mockChatServer.ts`：握手校验 Bearer（不对则 destroy）、无 Origin 断言、两种角色
  （`observer`：`bind_session → session_bound + 历史 run_event`，只推 `run_event`；`initiator`：`start_run → run_started` 再推发起者
  专属 op）、可脚本化推送、可强制断开连接（测重连）。
- vscode 薄层（`checklistTree`、`statusBar`、`approvals`、`deliverables`、`commands`）不写需要 mock `vscode` 的测试：
  它们的每个判定都落在 Step 1–4 的纯函数测试里；薄层靠 Step 8 的人工验收。
- 回归证明（harness 零改动）：`git status --porcelain -- pkg frontend npm ai-elements-vue cmd scripts .gitignore` 无输出。

## Done criteria

机器可核，全部成立：

- [ ] `(cd editors/vscode && npm run compile && npm run typecheck && npm test)` 全部 exit 0，且输出 `# tests 103`、`# pass 103`、`# fail 0`
      （子 shell，不改变 cwd；**其余各条命令均在仓库根执行**）
- [ ] `bash scripts/build-vscode-forebrain-ext.sh` exit 0，产出 `editors/vscode/out/forebrain-vscode-0.2.0.vsix`
- [ ] `code --install-extension editors/vscode/out/forebrain-vscode-0.2.0.vsix` 输出含 `Successfully installed`；
      `code --list-extensions --show-versions | grep forebrain.forebrain-vscode` 打印 `forebrain.forebrain-vscode@0.2.0`
- [ ] 零 harness 改动：`git status --porcelain -- pkg frontend npm ai-elements-vue cmd scripts .gitignore` 无输出
- [ ] 中继判定未被放宽：`git diff <派发时 376 落地所在的提交> -- editors/vscode/src/relay.ts` 无输出（派发方在 brief 里填该 SHA）
- [ ] 宿主 WS 不带 Origin：`grep -c "new WebSocket" editors/vscode/src/sessionSocket.ts` = 1，且该行选项只有 `headers`
      （authorization）与 `handshakeTimeout`、无 origin 键（人工核对该行；**不要用 `grep "{ origin"`**——构造器解构
      `constructor({ origin, … })` 是合规写法会误伤）；运行时证明由 sessionSocket.test.ts 用例 1（服务端没收到 origin）承担
- [ ] manifest 完整：
      `node -e "const p=require('./editors/vscode/package.json');const c=p.contributes;console.log(p.version,c.views.forebrain.map(v=>v.id).join(','),Object.keys(c.configuration.properties).join(','),c.commands.length,p.dependencies.ws)"`
      → `0.2.0 forebrain.chat,forebrain.checklist forebrain.gatewayUrl,forebrain.binaryPath,forebrain.autoStartGateway,forebrain.relayLogRequests,forebrain.selectionTarget,forebrain.includeDiagnostics,forebrain.approvalNotifications 7 8.23.0`
- [ ] `grep -n "sync_context" editors/vscode/src --include=*.ts -r` 无输出（那是网关里没有 handler 的保留 op，不得依赖）
- [ ] owner 验收清单（Step 8）1–10 全部勾选
- [ ] `plans/README.md` 本计划行状态更新（评审方维护）

## STOP conditions

遇到以下任一情况，停止并报告（不要绕路）：

- 前置不成立：376 未落地，或 drift check 任一条失败（含本计划引用的 harness 文件在 `3a261297` 之后变过）。
- 面板里 Web UI 的 WS 与扩展宿主的 WS 同绑一个会话时，观察到**任何一侧被断开／被踢／停止收事件**——这与 Current state 的事实依据
  （每条连接独立订阅）冲突。报告复现步骤与两侧的 Output／DevTools 输出，不要用「换一个会话」「结束前不 bind」这类办法绕开。
- 需要修改 `editors/vscode/src/relay.ts` 的任何判定才能继续。
- `startRun` 因 `turn_withdrawn` 且 `data.code` 为四个会话忙 code 之一（`session_running`／`session_awaiting_approval`／
  `session_running_elsewhere`／`session_awaiting_approval_elsewhere`）被拒：这是引擎的会话忙拒绝（另一个表面正在该会话里跑
  或等审批），**是预期行为**——显示服务端 `error` 文本，不重试；只有在没有别的表面运行时仍持续出现才 STOP。
- 键位冲突（Step 8 第 10 条）：删除默认 keybinding（保留命令），在报告里写明冲突的命令。
- 任一步的验证命令修复两次后仍失败。

## Maintenance notes

- **wire 契约**：`wsClientMsg` 的 json tag、`wsProtocolVersion`（`pkg/gateway/ws_protocol.go:13`）、`PlanUpdatedPayload`
  （`pkg/event/run_events.go:741-759`）、`requires_action.data` 的键、`RunEvent.type` 的取值、四个 REST 端点的返回形状。
  网关改任何一处，须同步 `sessionSocket.ts`／`sessionState.ts`（review checklist 项）。
- **「当前会话」是启发式**：靶子顺序 `observed → explicit → recent → 新建`（owner 裁决：面板当前显示的会话优先）。`observed` 依赖中继看到
  `GET /api/chat/sessions/<id>/messages`——SPA 换了加载路径（比如改成只走 `/events`）它就退化到 `recent`。届时把
  `sessionTarget.ts` 里的观察正则扩到新路径即可；不要为此改 harness。
- **多连接并存的事实依据没有专门的回归测试**：`handleChatWS` 每连接独立订阅（`server.go:1301-1312`、`:1836-1853`）。若将来网关
  引入「一个会话只留一条连接」，本扩展的宿主 WS 与面板会互踢——验收第 3 条能立刻暴露。
- **清单树只读**：勾选状态由代理的 `session_todo` 维护；在树上让用户改状态会与代理对账冲突，刻意不做。
- **重连游标**：`bind(cursor = lastSequence)` 依赖 `RunEvent.sequence`（`run_events.go:151-165`）；push-only 的流式事件
  （`assistant_delta` 等）没有 sequence，不进游标，也不该进。
- **后续（不属本计划）**：380 原生 Chat 参与者 `@Forebrain`（`vscode.chat.createChatParticipant` 自 `@types/vscode@1.100.0` 起稳定）；
  类 Claude Code 的隐藏 `ide` MCP 服务（让代理取诊断／开 diff）；「每轮自动附带选区上下文」（需 harness 侧上下文注入钩子）；
  取消运行的键位；单问题 `user_interaction` 的原生 QuickPick；`sync_context` 若引擎将来实现，本扩展是天然客户端。
