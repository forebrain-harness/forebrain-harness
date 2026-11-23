# Plan 113：网页与 TUI 同一套项目服务确认——会话里一屏勾选弹窗（MCP + 语言服务器）、项目页「总是允许本项目的服务」开关

> **执行者须知**：按步骤顺序执行，每步跑验证命令并确认预期结果后再进下一步。触发
> "STOP conditions" 时停下报告，不要即兴发挥。**不要更新 `plans/README.md`**（评审方维护索引）。
> **禁止提交代码**（owner 手动提交）：不 commit、不 push、不开 PR、不建分支、不用 worktree，改动直接留在 dev 工作区。
> `frontend/src/lib/api.ts`、`frontend/src/locales/index.ts`、`pkg/gateway/api_extra.go`、`docs/plan/WEB_REDESIGN_AND_GATEWAY_ACCESS/app-preview.html`
> **已有 owner 未提交的改动**：只加本计划要的内容，不回滚、不重排、不格式化别的部分。
>
> **owner 2026-10-24 裁决：不做原型，直接开发落地。** Part A 不执行，不改 `app-preview.html`；Part A 各步骤对界面（布局、状态、文案）的描述就是实现规格，
> Part B 里“按 / 1:1 还原 Part A 定稿”一律理解为“按 Part A 各步的描述实现”。直接从 Part B 开始。
>
> **前置**：`plans/110` 已落地：
> `grep -c 'func SetProjectServersAutoAllow(' pkg/process/mcp.go` → `1`；`grep -c 'func DecideProjectServers(' pkg/process/mcp.go` → `1`。否则 STOP 1。

## Status

- **Priority**: P2
- **Effort**: M–L（不做原型，只有 Part B）
- **Risk**: MED
- **Depends on**: `plans/110-project-servers-consent-engine.md`；与 `plans/112` 语义必须一致（可并行）
- **Category**: direction（owner 2026-10-23）
- **Planned at**: commit `f18ff14`（dev 工作区含 owner 未提交改动），2026-10-23

## Why this matters

owner 2026-10-23 把项目服务确认改成「一屏一次确认、不逐条问」，并规定 **TUI 是标准、网页向 TUI 看齐**、**MCP 与语言服务器语义一致**。TUI 的面板在 `112`。网页现在：

- 会话页的确认弹窗 `frontend/src/components/chat/ProjectMcpConsentDialog.vue` **逐行**「允许运行 / 不运行」按钮，**只管 MCP**，关掉弹窗 = 什么都不记；
- 项目页 MCP / LSP 标签没有「总是允许本项目的服务」开关——而 `110` 加的这个开关必须能在网页上关掉（owner 要求）。

目标语义（与 TUI 完全一致）：

| | TUI（`112`） | 网页（本计划） |
|---|---|---|
| 列什么 | 本项目全部待确认的 MCP + 语言服务器，分两组 | 同左 |
| 默认 | 全部勾选，「总是允许」不勾 | 同左 |
| 确认 | Enter：勾着的允许、没勾的不允许 | 「确定」按钮 |
| 全部不允许 | Esc（记下） | 「全部不允许」按钮；关闭弹窗（× / Esc / 点遮罩）同此 |
| 不记 | Ctrl+C | 无（网页没有对应操作） |
| 「总是允许本项目的服务」 | 勾上 ⇒ 全选；取消任一条 ⇒ 它取消 | 同左 |

项目页每行的「允许 / 拒绝」**仍只决定这一行**、拒绝可撤回（owner 2026-10-18 的规则不变）。

## Current state

**后端**（`pkg/gateway`）：

- 路由（`pkg/gateway/api_extra.go:247-256`）：`GET /:id/mcp`（`handleProjectMCPPreview`）、`POST /:id/mcp/consent`（`handleProjectMCPConsent`，`mcp_oauth_api.go:540`）、
  `GET /:id/lsp`（`handleProjectLSPPreview`）、`POST /:id/lsp/consent`（`handleProjectLSPConsent`，`api_extra.go:8025`）。
- 两个 consent 处理器：带 `decide` 时只决定这些条目（指纹必填，变了回 409，见 `writeStaleConsent`，`mcp_oauth_api.go:520`）；成功后 `pool.RebindAgent(s.Runner.AgentName)` 重建池 runner。
- 工作区取 `s.Runner.WorkspaceRoot`，项目上下文 `safety.ResolveRegisteredContext(s.Home, p.Root)`。
- `110` 已提供：`process.PendingProjectMCPConsents` / `process.PendingProjectLSPConsents`（都带 `Detail`、`Fingerprint`）、
  `process.DecideProjectServers(ws, launch, process.ProjectServersDecision{MCP, LSP, AlwaysAllow})`、
  `process.ProjectServersAutoAllowed(ws, launch)`、`process.SetProjectServersAutoAllow(ws, launch, on)`。

**前端**：

- 弹窗：`frontend/src/components/chat/ProjectMcpConsentDialog.vue`（`ForebrainModal` + `@repo/elements/confirmation`，逐行按钮）及其测试 `ProjectMcpConsentDialog.test.ts`。
- 会话页逻辑：`frontend/src/views/ChatView.vue:2408-2493`——`loadProjectMcpConsent` 读 `forebrainApi.mcpPage('project', …)` 里 `pending === true` 的行；
  `decideProjectMcpConsent` 逐条 POST 后 `rebindSessionObserver()`（网关在每次绑定时对齐 MCP）；`closeProjectMcpConsent` 只关弹窗。
- API：`frontend/src/lib/api.ts:3316`（`projectMcpConsent`）、`:3350`（`projectLspConsent`）；注意 `postJson` 会把**对象键**蛇形化，按名字作键的对象要改成数组传（现有注释已说明）。
- 文案：`frontend/src/locales/index.ts` 的 `chat.projectMcpConsent*`（中 `:1143-1147`、英 `:2938-2942`）。
- 项目页：`frontend/src/components/project/ProjectMcp.vue`（表格在 `components/mcp/McpServersTable.vue`）、`frontend/src/components/project/ProjectLsp.vue`。
- 设计稿：`docs/plan/WEB_REDESIGN_AND_GATEWAY_ACCESS/app-preview.html`（状态列表在约 `:4284` 的数组里，`mcpproj` = 项目 · MCP 三层，`mcpdecline` = 已拒绝可撤回；
  说明文字在约 `:1383`、`:2937`）。原型外框固定白/浅色背景。

**网页约定（必须遵守）**：新建/编辑表单用弹窗；文案简洁、能用标签表达就不写句子、**不新增解释性句子**；状态用图标；所有表格分页。

## Commands you will need

| Purpose | Command | Expected |
|---|---|---|
| 后端构建 | `go build ./...` | exit 0 |
| 后端测试 | `go test -tags fts5 ./pkg/gateway/ ./pkg/process/` | `ok` ×2 |
| 前端单测 | `cd frontend && npx vitest run src/components/chat src/components/project src/views/ChatView.test.ts src/lib/api.test.ts` | 全部通过 |
| 前端类型 | `cd frontend && pnpm typecheck` | exit 0 |
| 前端构建 | `cd frontend && pnpm build` | exit 0 |
| 格式 | `gofmt -l pkg/gateway` | 无输出 |

（写计划时 `frontend` 全量 vitest 在工作区有 2 例与本计划无关的红：`src/composables/useChatSessions.test.ts`，是 owner 进行中的改动；不要碰它。）

## Scope

**Part A**：不执行（只作界面规格，不改任何文件）。

**Part B**：
- `pkg/gateway/api_extra.go`（只加路由）、`pkg/gateway/project_servers_api.go`（新建）、`pkg/gateway/project_servers_api_test.go`（新建）
- `frontend/src/components/chat/ProjectServersConsentDialog.vue`（新建，取代 `ProjectMcpConsentDialog.vue`，后者与其测试删除）及测试
- `frontend/src/views/ChatView.vue`（只改项目确认那一段）及其测试中相关用例
- `frontend/src/components/project/ProjectMcp.vue`、`ProjectLsp.vue`（加开关）及测试
- `frontend/src/lib/api.ts`（加 3 个方法）、`frontend/src/locales/index.ts`（换文案键）

**Out of scope**：设置 · MCP / 主代理 MCP 页的状态列（`114`）；TUI（`112`）；`process` 判定逻辑（`110`）；项目页表格每行的允许/拒绝行为（不变）。

## Part A：界面规格（不改原型；下面描述的界面即 Part B 的实现依据，其中“在 `app-preview.html` 加状态/截图”的动作一律不做）

### Step A1：会话里的确认弹窗

在 `app-preview.html` 加一个状态（加进约 `:4284` 的状态数组，名字 `会话 · 项目服务确认（弹窗）`，key 自取如 `projsrv`），画在会话页上：

```
┌ 项目服务 ───────────────────────────────── × ┐
│ 3 个服务要在本项目运行                          │
│                                                │
│ MCP                                            │
│ ☑ caveman-shrink   npx -y caveman-shrink codegraph serve --mcp │
│ ☑ github           https://api.example.com/mcp │
│ 语言服务器                                      │
│ ☑ gopls            gopls serve                 │
│                                                │
│ ☐ 总是允许本项目的服务                          │
│                                                │
│                      [全部不允许]  [确定]       │
└────────────────────────────────────────────────┘
```

- 标题 `项目服务`；副标题 `N 个服务要在本项目运行`（只有数字变）；组标题 `MCP`、`语言服务器`；命令/地址用等宽字体、浅色；
  底部按钮 `全部不允许`（次要）与 `确定`（主要）。**不加任何说明句子。**
- 再加一个状态展示「勾上总是允许 ⇒ 全部勾选」。
- 英文文案对应：`Project servers` / `N servers want to run in this project` / `MCP` / `Language servers` / `Always allow this project's servers` / `Decline all` / `Confirm`。

### Step A2：项目页的开关

在 `mcpproj`（项目 · MCP 三层）与项目 · LSP 标签的表格工具栏**左侧**加开关 `总是允许本项目的服务`（两个标签显示的是同一个开关）；
加一个状态展示开关打开时：原来「待确认」的项目行显示为已允许（徽标沿用现有「已允许」）。

### Step A3：说明区

把约 `:1383` 那条「项目 MCP 的确认（owner 2026-10-18）」改写为新语义（一屏勾选、默认全选、全部不允许、总是允许开关、每行仍只决定这一行），约 `:2937` 的 MCP 标签描述补「工具栏左侧：总是允许本项目的服务」。


## Part B：实现

### Step B1：网关三个接口

新建 `pkg/gateway/project_servers_api.go`，路由在 `api_extra.go` 的 projects 组里、`/:id/lsp/consent` 之后注册：

1. `GET /:id/servers/pending` → `{"mcp":[{"name","detail","fingerprint"}],"lsp":[{"name","detail","fingerprint"}],"auto_allow":bool}`
   （`process.PendingProjectMCPConsents` / `PendingProjectLSPConsents`，LSP 的 `name` 填 `ID`；`auto_allow` = `process.ProjectServersAutoAllowed`）。
2. `POST /:id/servers/consent`，body `{"mcp":[{"name","fingerprint","allow"}],"lsp":[...],"always_allow":bool}` → `process.DecideProjectServers`；
   stale → `writeStaleConsent`（409）；成功 → 与现有 consent 处理器相同地 `pool.RebindAgent(s.Runner.AgentName)`，回 `{"ok":true}`。
3. `PUT /:id/servers/auto-allow`，body `{"on":bool}` → `process.SetProjectServersAutoAllow`；成功同样 `RebindAgent`，回 `{"ok":true,"on":bool}`。

项目查找、未找到 404、`ResolveRegisteredContext` 失败 400、工作区来源都照 `handleProjectMCPConsent` 写（复制它的前半段结构）。
`GET /:id/mcp` 与 `GET /:id/lsp` 的响应各加一个 `auto_allow` 字段（项目页开关读它）。

**Verify**：`go build ./... && go test -tags fts5 ./pkg/gateway/` → `ok`。

### Step B2：前端 API 与文案

`api.ts` 加 `projectServersPending(id)`、`projectServersConsent(id, body)`、`projectServersAutoAllow(id, on)`（数组形参，避开键名蛇形化）；`ProjectMcpRecord`/`ProjectLspRecord` 等类型加 `autoAllow?: boolean`。
`locales/index.ts`：删 `chat.projectMcpConsent*` 五个键，加新键（中英）对应 Part A 定稿的文案。

### Step B3：会话弹窗

新建 `ProjectServersConsentDialog.vue` 1:1 还原 Part A 定稿；props：`open`、`mcp`、`lsp`、`busy`、`error`；emits：`confirm({allowed: Record<…>, alwaysAllow})`、`declineAll`。
勾选联动与 TUI 相同。`ChatView.vue`：读 `projectServersPending`，有待确认项就打开；`confirm` / `declineAll` / 关闭（× / Esc / 遮罩）都调 `projectServersConsent`（关闭 = 全部不允许）；
409 时重新读列表、保持打开并显示错误；成功后 `rebindSessionObserver()`（沿用现有理由注释）。删 `ProjectMcpConsentDialog.vue` 及其测试。

### Step B4：项目页开关

`ProjectMcp.vue`、`ProjectLsp.vue` 工具栏左侧加开关（读 `auto_allow`，切换调 `projectServersAutoAllow`，成功后刷新表格）。

### Step B5：测试

见 Test plan。

**Verify（Part B）**：后端、前端各命令全部通过；`grep -rn "ProjectMcpConsentDialog\|projectMcpConsentTitle" frontend/src` 无输出。

### Step B6：真机验收

按 `plans/112` Step 7 同样的临时项目（两条 MCP + 一条 LSP），用 `forebrain gateway start` 打开网页、把该目录加为项目、在项目下新建会话：
弹窗按 Part A 描述出现；取消一条后「确定」→ 项目页 MCP / LSP 标签对应行为已允许 / 已拒绝；再次新建会话不再弹出；在项目页打开开关并往 `mcp_servers.yaml` 追加一条 → 新会话不弹窗、新条目显示已允许；
关掉开关 → 新条目回到待确认，新会话再次弹窗。截图存临时目录并在报告里列出。

## Test plan

- `pkg/gateway/project_servers_api_test.go`（模式参照现有 consent 处理器的测试，`grep -n "mcp/consent" pkg/gateway/*_test.go` 找）：
  pending 返回两组与 `auto_allow`；consent 记录正确且 `always_allow` 打开开关；指纹不符 → 409 `consent_stale`；auto-allow 开/关往返；未知项目 404。
- 前端：`ProjectServersConsentDialog.test.ts`（默认全选、联动两条、确定的载荷、全部不允许、关闭等同全部不允许）；`ChatView.test.ts` 中项目确认相关用例改为新接口；
  `ProjectMcp.test.ts` / `ProjectLsp.test.ts` 加开关的读与写。

## Done criteria

- [ ] `go build ./...`、`go test -tags fts5 ./pkg/gateway/ ./pkg/process/` 通过；`gofmt -l pkg/gateway` 无输出
- [ ] `cd frontend && pnpm typecheck && pnpm build` 通过；本计划涉及的 vitest 全部通过
- [ ] 旧弹窗与旧文案键已删除（上面的 grep 无输出）
- [ ] Step B6 各项通过并有截图

## STOP conditions

1. 前置（`110`）未落地。
2. 现有项目页「每行只决定这一行」的测试因本计划变红。

## Maintenance notes

- 网页与 TUI 的确认语义是同一份规则（见本计划的语义表）；以后改任何一边，另一边要同改。
- 新接口与旧的逐条 consent 接口并存：项目页每行仍用旧接口（带 `decide`）。
