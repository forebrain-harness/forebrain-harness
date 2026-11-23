# Plan 077：071–076 执行后复核补丁——项目 MCP/LSP 逐条确认且“拒绝”可撤回、终端 N 的文案、全局 MCP“未运行 · 被覆盖”、连接状态取值、子代理“所在会话”指向父对话

> **执行者须知**：按步骤顺序执行，每步跑验证命令并确认预期结果后再进下一步。触发
> "STOP conditions" 时停下报告，不要即兴发挥。**不要更新 `plans/README.md`**（评审方维护索引）。
> **禁止提交代码**（owner 手动提交）：不建分支、不建 worktree、不 commit、不 push、不 stash；改动留在当前工作区。
> 先跑 `scripts/fetch-libtokenizers.sh`；Go 测试要 pdfium：照 Makefile 导出环境变量（见 “Commands you will need”），否则 `pkg/process`、`pkg/gateway` 报 `Package 'pdfium' not found`、编译失败。
>
> **背景**：071–076 已执行，改动在工作区**未提交**（约 200 个文件）。本计划只修复核确认的缺口，**不重做** 071–076 的任何一步，也不回改那六份计划文件。
> 行号按 2026-10-18 的工作区（HEAD `7e11f9a` + 未提交改动），**一律用给出的代码片段 grep 定位**，行号只作参考。
>
> **Drift check（先跑）**：下面每条都必须**各有输出**，否则 STOP（说明代码已经变了，本计划的前提不成立）：
> ```
> grep -nF 'process.DecideProjectMCPConsents(workspace, launch, body.Allow)' pkg/gateway/mcp_oauth_api.go
> grep -nF 'process.DecideProjectLSPConsents(workspace, launch, body.Allow)' pkg/gateway/api_extra.go
> grep -nF 'row["pending"] = true' pkg/gateway/mcp_oauth_api.go
> grep -nF 'forebrainApi.projectMcpConsent(projectId, allow ? [row.name] : [])' frontend/src/components/mcp/McpServersTable.vue
> grep -nF 'forebrainApi.projectLspConsent(props.projectId, allow ? [id] : [])' frontend/src/components/project/ProjectLsp.vue
> grep -nF "'mcp.consentAllTip'" frontend/src/locales/index.ts
> grep -nF 'skipped — confirm later' cmd/forebrain/mcp_consent.go
> grep -nF 'SessionID:      strings.TrimSpace(entry.WorkerSessionID),' pkg/gateway/agents_api.go
> grep -nF "if (row.running === true) return { tone: 'ok', label: \`\${t('mcp.status.connected')}" frontend/src/components/mcp/mcpRows.ts
> ```
> 倒数第二条应有 2 行（MCP 与 LSP 各一行）。

## Status

- **Priority**: P1（第 1 项是数据错误：网页上点一行“允许”会把同项目其它待确认条目**记成拒绝**，且拒绝后再也启用不了）
- **Effort**: M
- **Risk**: MED（改动项目 MCP/LSP 的确认存储写入路径；终端启动提示只改文案，不改行为）
- **Depends on**: 071–076 已执行（工作区现状）
- **Category**: correctness + direction（设计稿落地）
- **Planned at**: commit `7e11f9a` + 工作区未提交改动，2026-10-18

## Why this matters

2026-10-18 评审方对照工作区代码复核 071–076 的执行结果。大部分已按计划落地并通过测试（`vue-tsc` exit 0、vitest 116 文件 745 例全过；`go test ./pkg/gateway ./pkg/process ./pkg/lsp ./cmd/forebrain ./pkg/state ./pkg/config` 全 ok；`go vet`、`gofmt` 无输出）。
073 Step 7 的三处偏差里，工具入参与搜索框高度已修好；072 遗留的工坊面包屑截断、`App.vue` 的原生 `title` 也已修好。**仍然存在**的问题如下：

1. **项目 MCP / 项目 LSP 的“允许 / 拒绝”一次决定全部**：网页每行只传一个名字，后端 `DecideProjectMCPConsents` / `lsp.DecideProjectServers` 却把**所有**没传的待确认条目都记成拒绝。074 执行时没有改后端，而是在徽标上挂了一句提示（`mcp.consentAllTip`：“允许或拒绝其中一条，会把其它待确认项一并记为拒绝”）。
   **owner 2026-10-18 裁定**：网页上每行的允许/拒绝**只决定这一条**。
2. **拒绝无法撤回**：两个决定函数都会跳过“已经决定过”的条目，所以一条被拒绝的条目（包括终端启动时答 N、直接回车的）只有改了配置（指纹变化）才会再问。
   项目 MCP 表格里，已拒绝的条目**根本不显示**，只在表下的说明里出现一次。
   **owner 2026-10-18 裁定**：N = 拒绝（记下、下次启动不再问），但**可以撤回**：网页项目空间的 MCP / LSP 标签显示“已拒绝”的行，并给一个“允许”按钮（原型 v57 已画）。
3. **终端文案与行为不符**：答 N 时终端打印 `skipped — confirm later to load it`（LSP 是 `… or at the next start`），实际却已经记成拒绝，下次启动也不会再问。
4. **设置 · MCP 缺“未运行 · 被覆盖”**：全局条目被主代理同名条目覆盖时，原型（`app-preview.html` 里搜 `未运行 · 被覆盖`）显示灰徽标 `未运行 · 被覆盖`，悬停提示 `被主代理 {名} 覆盖，未运行`；实现里只显示 `未运行`，看不出原因。
5. **连接状态取值不全**：`mcpRows.ts` 的 `mcpStatus` 只要 `running === true` 就显示 `已连接`。运行时的连接状态（`pkg/mcp/registry.go` `ConnStatus`）有 `unknown/connecting/connected/disconnected/error/cancelled` 六种，`connecting`、`disconnected`、`cancelled`（用户跳过的可选服务）都会被误显示为“已连接”。
6. **子代理“所在会话”指错了会话**（即 073 Step 7.2，未执行）：子代理表格的“所在会话”取的是子代理自己的 worker 会话；原型里这一列是子代理所在的那条对话（父对话）。

## 设计稿（验收依据，实施须 1:1 还原）

- 原型 https://claude.ai/artifact/KKfPW8dXWScuRdTzonsk2F（本地 `docs/plan/WEB_REDESIGN_AND_GATEWAY_ACCESS/app-preview.html`，v57）。
- 状态按钮：**项目 · MCP**、**项目 · MCP 已拒绝可撤回**（`mcpdecline`）、**设置 · MCP**、**子代理**。
- 原型代码（在文件里搜函数名）：
  - `mcpState`：`declined` → `['gray', '已拒绝', '']`。
  - `mcpStatusCell`：
    - `r.over` → `<span class="badge gray" title="被主代理 {over} 覆盖，未运行">未运行 · 被覆盖</span>`；
    - `declined` 的提示是 `不会加载；点“允许”撤回`；
    - `pending` 没有提示。
  - `mcpEnableCell`：`pending` 与 `declined` 的启用格**留空**。
  - `mcpActions`：
    - `pending` → `允许`（`check`，ok）+ `拒绝`（`x`，dgr）；
    - `declined` → `允许`（`check`，ok）+ 空位（`actSlot()`）。
  - 抽屉状态框（搜 `row.over ?`）：被覆盖时徽标是 `未运行`，旁边灰字 `被主代理 {over} 覆盖`。
  - 项目 MCP 原型数据（搜 `st: 'declined'`）：项目层的顺序是 待确认 → 已允许 → 已拒绝。
  - 子代理页（搜 `<th>所在会话</th>`）：“所在会话”是对话标题链接，点了进入那条对话。
- 原型说明（搜 `项目 MCP 的确认（owner 2026-10-18）`）：
  > 每行的“允许 / 拒绝”只决定这一行；拒绝过的条目（含终端启动时答 N 的）不再从表里消失，显示“已拒绝”，操作栏给“允许”可以撤回。项目 LSP 按同一规则。

## Current state（2026-10-18 工作区，已核实）

**存储与决定函数**
- `pkg/mcp/project_consent.go`：
  - `ProjectConsents` 的结构是 项目键 → 小写名 → `{fingerprint, decision, decided_at}`；
  - `Decision(projectKey, name, fp)` 只认**同一指纹**的记录；
  - `Decide(projectKey, name, fp, decision, at)` 直接覆盖；
  - 常量 `ProjectConsentAllow = "allow"`、`ProjectConsentDeny = "deny"`；`ConsentSummary(srv)` 生成一行摘要。
- `pkg/process/mcp.go`：
  - `ResolveSessionMCP` 里，已拒绝的条目进 `not_applied`，原因是 `"entry was declined for this project"`，待确认的原因是 `"awaiting confirmation"`。
  - `PendingProjectMCPConsents(agentWorkspace, launch)` 列出没有决定的条目。
  - `DecideProjectMCPConsents(agentWorkspace, launch, allowed)` 的现状代码：
    ```go
    for _, srv := range entries {
        fp := mcp.ServerFingerprint(srv)
        if _, decided := consents.Decision(projectKey, srv.Name, fp); decided {
            continue
        }
        decision := mcp.ProjectConsentDeny
        if _, ok := allowedSet[strings.ToLower(strings.TrimSpace(srv.Name))]; ok {
            decision = mcp.ProjectConsentAllow
        }
        consents.Decide(projectKey, srv.Name, fp, decision, time.Now())
    }
    return mcp.SaveProjectConsents(agentWorkspace, consents)
    ```
  - `DecideProjectLSPConsents` 转调 `lsp.DecideProjectServers(agentWorkspace, root, memory.ProjectKey(root), allowed)`。
  - `InspectProjectLSP` 返回 `trusted, pending, allowed, denied, notes`。
- `pkg/lsp/project.go` `DecideProjectServers(agentWorkspace, projectRoot, projectKey, allowed)` 与上面同构：
  - 遍历 `sortedServerIDs(entries)`，跳过已决定的条目，其余没在 `allowed` 里的记成拒绝；
  - 用到的函数：`LoadProjectServers(root)` 返回 `map[id]LSPServerConfig`，`ProjectServerFingerprint`，`LoadProjectConsents`/`SaveProjectConsents`。

**gateway**
- `pkg/gateway/mcp_oauth_api.go`：
  - `handleProjectMCPConsent`：body 只有 `Allow []string`，直接调 `process.DecideProjectMCPConsents(workspace, launch, body.Allow)`，然后 `pool.RebindAgent(...)`。
  - `handleProjectMCPPreview`：
    - 带 `page` 时，行的顺序是 `mcpPendingPreviewRows(pending, launch)`（`row["pending"] = true`、`running=false`、`summary`）→ 项目层已同意的行（`mcpPreviewRow(srv)` + `row["allowed"] = true`）→ 主代理层 → 全局层；
    - 用 `writeMCPRows(w, q, mcpLayerFilter(r.URL.Query().Get("source")), rows, extra)` 输出；
    - `extra` 里带顶层 `overridden_global`、`not_applied`、`pending_consent`。
  - **注意**：`mcpServerRow`（`mcp_write_api.go`）也会给主代理/全局行写 `row["pending"] = rc.runtime.Pending`，意思是“这一代 MCP 启动尚未完成”。所以前端判定“待确认”时同时要求 `scope === 'project' && allowed !== true`。本计划的“已拒绝”**不复用 `pending`**，新增 `declined` 键。
- `pkg/gateway/api_extra.go`：
  - `handleProjectLSPConsent`：body 只有 `Allow`，调 `process.DecideProjectLSPConsents`。
  - `handleProjectLSPPreview` 带 `page` 时给 `rows: [{id, summary, status}]`，`status` 取 `pending`/`allowed`/`denied`，已拒绝的行**已经在表里**（`summary` 为空串）。
- `pkg/gateway/mcp_write_api.go`：
  - `handleMCPLayerList(w, r, lc, rc, scope)` 列一层，被覆盖的全局条目照列，`running=false`，没有任何“被覆盖”字段。
  - `handleMCPAgentView` 用名字集合 `shadowed` 隐藏被主代理覆盖的全局条目，写法可以照抄。

**终端**
- `cmd/forebrain/mcp_consent.go`：
  - `ensureProjectMCPConsent` 逐条问 `Run it? [y/N] `，`y`/`yes` → `  confirmed — it loads this session`，其余 → `  skipped — confirm later to load it`，问完调 `DecideProjectMCPConsents`；
  - `ensureProjectLSPConsent` 问 `Allow it? [y/N] `，否 → `  skipped — confirm later on the project page or at the next start`；
  - 读输入出错且没读到内容时直接 `return nil`，不记录，所以这些条目仍然待确认。
  - 文件头注释与 LSP 函数注释也写着“以后再确认”。
- `pkg/lsp/control.go`：已拒绝条目的提示是 `"project entry declined; it asks again when .forebrain/lsp_servers.yaml changes"`，`pkg/lsp/control_test.go` 断言了这句原文。

**前端**
- `frontend/src/lib/api.ts`：
  - `projectMcpConsent(id: string, allow: string[])` 与 `projectLspConsent(id: string, allow: string[])` 都用 `postJson`；它会对 body 做 `toSnakeCase`，但只转键，数组里的名字不变。
  - `McpServerRow` 已有 `pending?`、`allowed?`、`summary?`、`connStatus?`、`running?`；
  - `mcpRowFromWire` 按 `if ('pending' in wire) row.pending = flag('pending')` 逐字段映射原始 JSON（MCP 读接口绕开了驼峰拦截器，**新字段也必须在这里手动映射**）。
- `frontend/src/components/mcp/mcpRows.ts` `mcpStatus` 的现有顺序是：
  1. 待确认；
  2. `disabledByConfig`；
  3. `disabledNextSession`；
  4. 失败（`connStatus` 为 `error`/`failed`，或 `error` 非空）；
  5. 项目层 `allowed` → `已允许`；
  6. `running` → `已连接`；
  7. 其余 → `未运行`。
- `frontend/src/components/mcp/McpServersTable.vue`：
  - `TableRow` 有 `pending`；`tip: pending ? t('mcp.consentAllTip') : status.tip`；
  - 启用格条件是 `v-else-if="!entry.pending"`；
  - 操作栏 `<template v-if="entry.pending">` 给 允许 / 拒绝（testid `mcp-allow-${key}` / `mcp-decline-${key}`）；
  - `decide(row, allow)` 调 `projectMcpConsent(projectId, allow ? [row.name] : [])`；
  - 表下的 `mcp-not-applied` 段落把 `not_applied` 逐条写出来：`{name} — {reason}`。
- `frontend/src/components/mcp/McpServerDrawer.vue`：
  - 状态框是 `<BadgeComponent :variant="statusVariant" data-testid="mcp-state-status">{{ status.label }}</BadgeComponent>`；
  - 后面跟灰字 `<span class="mcp-key">`，用来写 `enabled: false` 或“下个会话起不加载”。
- `frontend/src/components/project/ProjectLsp.vue`：
  - 只有 `row.status === 'pending'` 的行有 允许 / 拒绝（testid `lsp-allow-${id}` / `lsp-decline-${id}`），其余行是两个 `RowActionSlot`；
  - `consent(id, allow)` 调 `projectLspConsent(props.projectId, allow ? [id] : [])`；
  - 徽标没有提示。
- 子代理：
  - `pkg/gateway/agents_api.go` `subagentRosterRow(entry agent.HistoryEntry)` 写的是 `SessionID: strings.TrimSpace(entry.WorkerSessionID)`；
  - `handleAgentRoster` 用 `s.Sessions.SessionTitle(r.Context(), record.SessionID)` 填 `session_title`；
  - `agent.HistoryEntry`（`pkg/agent/subagent_history.go`）同时有 `SessionID`（父对话）和 `WorkerSessionID`；
  - `config.RosterRow`（`pkg/config/primary_agent.go`）没有父会话字段；
  - 前端 `views/SubagentsView.vue` 的“所在会话”按钮与操作栏 `打开会话` 都调 `jumpToSession(row)`，即 `agentRosterViewTarget(row)`（`composables/useAgentRoster.ts`）→ `{ path: '/', query: { session: row.sessionId } }`。
  - `agentRosterViewTarget` 也被工作台概览的 `AgentRosterPanel` 使用，**不要改它的语义**。

**现有测试（会受影响）**
- `pkg/gateway/api_extra_test.go`：
  - `TestProjectMCPPreviewAndConsentEndpoint`、`TestProjectMCPPreviewOrdersProjectAgentGlobalAndIncludesPending`；
  - fixture `newMCPPreviewFixture`：项目 d 已同意、e 待确认，主代理 b，全局 a；辅助函数 `allowProjectMCPEntry`；
  - `TestProjectLSPPreviewAndConsentEndpoint`、`TestProjectLSPPreviewPagedAndWhole`；
  - `TestMCPAgentViewHidesShadowedGlobalAndListsAgentFirst`：全局 a、b，主代理 b、c。
- `pkg/process/mcp_test.go`：`TestResolveSessionMCPDeniedExcluded` 等，辅助函数 `trustedVCSProject`、`writeProjectMCP`、`resolveMCPGlobal`。
- `pkg/lsp/project_test.go`：`TestDecideProjectServers`。
- `cmd/forebrain/interactive_test.go`：`TestEnsureProjectLSPConsentPrompt` 断言了 `skipped — …` 原文。
- 前端单测：
  - `McpServersTable.test.ts`：约 229 行断言 `toHaveBeenCalledWith('proj-1', ['e'])`；
  - `ProjectLsp.test.ts`：约 106 / 115 行断言 `('p1', ['projectls'])` / `('p1', [])`；
  - 另有 `mcpRows.test.ts`、`McpServerDrawer.test.ts`、`SubagentsView.test.ts`、`useAgentRoster.test.ts`。
- e2e `frontend/e2e/mcp-pages.spec.ts`：
  - 种数据 `seedProjectLayers` 先建 d、用**不带 `decide`** 的同意接口允许 d，再建 e。这一调用的语义本计划不变，种数据不用改。
  - `seedSharedLayers`：全局 a、b，主代理 b、c。
  - `e2e/chat-composer.spec.ts` 也用不带 `decide` 的同意接口。

## Commands you will need

在仓库根目录：
```
P=$PWD/third_party/pdfium/$(go env GOOS)_$(go env GOARCH)
export PKG_CONFIG_PATH=$P CGO_CFLAGS="-I$P/include" CGO_LDFLAGS="-L$P/lib -lpdfium"
```
（`third_party/pdfium/<平台>` 不存在时先 `make pdfium pdfium-fonts`）

| 用途 | 命令 | 成功预期 |
|------|------|----------|
| Go 全部相关包 | `CGO_ENABLED=1 go test -tags fts5 ./pkg/process ./pkg/lsp ./pkg/gateway ./pkg/config ./cmd/forebrain -count=1` | 每行 `ok` |
| vet / fmt | `CGO_ENABLED=1 go vet -tags fts5 ./pkg/gateway ./pkg/process ./pkg/lsp ./cmd/forebrain`；`gofmt -l pkg cmd` | 均无输出 |
| 前端单测 | `cd frontend && pnpm test` | 全绿 |
| 类型检查 | `cd frontend && npx vue-tsc --noEmit -p tsconfig.json` | exit 0 |
| e2e 单文件 | `FOREBRAIN_E2E_SPEC=e2e/mcp-pages.spec.ts scripts/acceptance/web_e2e.sh` | 末行 `web e2e: PASS` |
| e2e 全套 | `scripts/acceptance/web_e2e.sh` | 末行 `web e2e: PASS` |

> 注意：Go 的 `-run` 是正则，多个名字用单个 `|` 连接（如 `-run 'A|B'`），**不要写成 `\|`**——那会被当成字面竖线，一个测试都不匹配却显示 `ok`（假通过）。所以各步 Verify 都再数一遍 `--- PASS` 的行数，确认新测试真的跑了。

开工前先 `git status --short > $TMPDIR/077-before.txt`（Done criteria 要用）。

## Scope

**In scope**
- Go：
  - `pkg/process/mcp.go` 与 `pkg/process/mcp_test.go`；
  - `pkg/lsp/project.go` 与 `pkg/lsp/project_test.go`；
  - `pkg/lsp/control.go` 与 `pkg/lsp/control_test.go`（只改已拒绝提示的文案）；
  - `pkg/gateway/mcp_oauth_api.go`、`pkg/gateway/mcp_write_api.go`（只改 `handleMCPLayerList`）、`pkg/gateway/api_extra.go`（只改 `handleProjectLSPConsent`）、`pkg/gateway/agents_api.go`；
  - `pkg/gateway/api_extra_test.go`、`pkg/gateway/agents_api_test.go`；
  - `pkg/config/primary_agent.go`（只给 `RosterRow` 加一个字段）；
  - `cmd/forebrain/mcp_consent.go` 与 `cmd/forebrain/interactive_test.go`。
- 前端：
  - `frontend/src/lib/api.ts`（两个同意方法、`McpServerRow` 与 `mcpRowFromWire`、`AgentRosterRow`）及 `frontend/src/lib/api.test.ts`；
  - `frontend/src/components/mcp/{mcpRows.ts,mcpRows.test.ts,McpServersTable.vue,McpServersTable.test.ts,McpServerDrawer.vue,McpServerDrawer.test.ts}`；
  - `frontend/src/components/project/{ProjectLsp.vue,ProjectLsp.test.ts}`；
  - `frontend/src/composables/{useAgentRoster.ts,useAgentRoster.test.ts}`（**只新增**一个函数）；
  - `frontend/src/views/{SubagentsView.vue,SubagentsView.test.ts}`；
  - `frontend/src/locales/index.ts`；
  - `frontend/e2e/mcp-pages.spec.ts`。

**Out of scope**（看起来相关，但**不要碰**）
- 终端启动提示的**行为**：仍是逐条问、问完调用原来的 `DecideProjectMCPConsents` / `DecideProjectLSPConsents`，`y` 允许，其余拒绝，读输入出错不记录。**只改打印的文案与注释**。
- `DecideProjectMCPConsents` / `lsp.DecideProjectServers` 本身的语义（终端与旧调用方仍用它们）。
- `ResolveSessionMCP` 与 `not_applied` 的原因文字（TUI 的 `/mcp` 也读它）。
- 不带 `page` 的 `/api/v1/projects/:id/mcp`、`/api/v1/projects/:id/lsp` 的响应（聊天页等调用方按旧形状读）。
- 项目 LSP“已允许”行的撤回（owner 只裁定了“拒绝可撤回”，没有裁定“允许可撤回”）。
- 按层筛选的查询参数名 `source`（已能正确按行的 `scope` 过滤，改名只会多改调用方）。
- 071–076 的计划文件与 `plans/README.md`。

## Steps

### Step 1：存储层——只决定点名的条目，可以改写已有决定；列出已拒绝的条目

1. `pkg/process/mcp.go`，在 `DecideProjectMCPConsents` 后面新增：
   ```go
   // DecideProjectMCPConsentsFor records a decision for the named project
   // entries only — allow when the name is also in allowed, deny otherwise —
   // and leaves every other entry exactly as it was. Unlike the startup
   // prompt's DecideProjectMCPConsents it overwrites a decision a named entry
   // already has: that is how a declined entry is allowed again.
   func DecideProjectMCPConsentsFor(agentWorkspace string, launch safety.ProjectContext, decide, allowed []string) error
   ```
   实现要点：
   - `root := safety.TrustedRoot(launch)`，为空返回 `nil`；项目键 `memory.ProjectKey(root)`；条目来自 `mcp.LoadProjectMCPServers(root)`。
   - `mcp.LoadProjectConsents(agentWorkspace)` **出错时直接返回该错误**，不要用空表去覆盖一个读不出来的文件。
   - `decide`、`allowed` 都按 `strings.ToLower(strings.TrimSpace(name))` 建集合。
   - 遍历条目，名字不在 `decide` 里就跳过；在的话按是否在 `allowed` 里记 `ProjectConsentAllow` 或 `ProjectConsentDeny`，指纹用 `mcp.ServerFingerprint(srv)`，时间用 `time.Now()`。**不检查是否已决定**。
   - 文件里没有的名字忽略，不报错。
   - 最后 `mcp.SaveProjectConsents`。
2. 同文件新增 `DeclinedProjectMCPConsents(agentWorkspace string, launch safety.ProjectContext) []mcp.PendingProjectConsent`：
   - 未信任返回 `nil`，存储读失败返回 `nil`；
   - 列出**当前指纹**下决定为 `ProjectConsentDeny` 的条目，字段是 `Name`、`Fingerprint`、`Summary: mcp.ConsentSummary(srv)`；
   - 顺序同项目文件，项目根与项目键的取法同第 1 点。
3. `pkg/lsp/project.go`，在 `DecideProjectServers` 后面新增 `DecideProjectServersFor(agentWorkspace, projectRoot, projectKey string, decide, allowed []string) error`，语义同第 1 点：
   - 遍历 `sortedServerIDs(entries)`；
   - 只处理 `decide` 里的 id，可以改写已有决定；
   - 存储读失败返回错误。
4. `pkg/process/mcp.go`，在 `DecideProjectLSPConsents` 后面新增 `DecideProjectLSPConsentsFor(agentWorkspace string, launch safety.ProjectContext, decide, allowed []string) error`：
   - 未信任返回 `nil`；
   - 否则转调 `lsp.DecideProjectServersFor(agentWorkspace, root, memory.ProjectKey(root), decide, allowed)`。
5. 测试。
   - `pkg/process/mcp_test.go`，照 `TestResolveSessionMCPDeniedExcluded` 的造法（`trustedVCSProject`、`writeProjectMCP`、`resolveMCPGlobal`）：
     - `TestDecideProjectMCPConsentsForLeavesOthersUndecided`：项目两条 `one`、`two`；`DecideProjectMCPConsentsFor(ws, launch, []string{"one"}, nil)` 之后，`PendingProjectMCPConsents` 只剩 `two`，`DeclinedProjectMCPConsents` 只有 `one`。
     - `TestDecideProjectMCPConsentsForAllowsADeclinedEntry`：先 `DecideProjectMCPConsents(ws, launch, nil)`（全拒），再 `DecideProjectMCPConsentsFor(ws, launch, []string{"one"}, []string{"one"})`；
       之后 `resolveMCPGlobal(...)` 的服务里有 `one`，`DeclinedProjectMCPConsents` 只剩 `two`。
     - `TestDeclinedProjectMCPConsentsNeedTrust`：未信任的项目返回空。
   - `pkg/lsp/project_test.go`，照 `TestDecideProjectServers`：`TestDecideProjectServersForOnlyTouchesNamed`（不点名的条目仍待确认）与 `TestDecideProjectServersForAllowsADeniedEntry`（先全拒、再点名允许，`ResolveProjectServers(...).Allowed` 里有它）。
   - `pkg/process/mcp_test.go` 加 `TestDecideProjectLSPConsentsForAllowsADeniedEntry`，造法照 `TestProjectLSPConsentsNeedTrust`。

**Verify**：
```
CGO_ENABLED=1 go test -tags fts5 ./pkg/process -run 'DecideProjectMCPConsentsFor|DeclinedProjectMCPConsents|DecideProjectLSPConsentsFor' -count=1 -v 2>&1 | grep -c '^--- PASS'
CGO_ENABLED=1 go test -tags fts5 ./pkg/lsp -run 'DecideProjectServersFor' -count=1 -v 2>&1 | grep -c '^--- PASS'
```
第一条输出 `4`，第二条输出 `2`（新测试确实跑了且通过）。随后 `CGO_ENABLED=1 go test -tags fts5 ./pkg/process ./pkg/lsp -count=1` 两行 `ok`，`gofmt -l pkg` 无输出。

### Step 2：gateway——同意接口加 `decide`；项目 MCP 表格带“已拒绝”行；全局层标出“被覆盖”

1. `handleProjectMCPConsent`（`mcp_oauth_api.go`）：
   - body 改为：
     ```go
     var body struct {
         Allow  []string  `json:"allow"`
         Decide *[]string `json:"decide"`
     }
     ```
     **`Decide` 用指针**：没有这个键就是 `nil`，走旧函数；`"decide": []` 是“什么都不决定”，走新函数后不改任何记录。
   - `body.Decide != nil` 时调 `process.DecideProjectMCPConsentsFor(workspace, launch, *body.Decide, body.Allow)`，否则调原来的 `process.DecideProjectMCPConsents(workspace, launch, body.Allow)`。
   - 后面的 `RebindAgent` 与响应不变。
2. `handleProjectLSPConsent`（`api_extra.go`）同样改：有 `decide` 时调 `process.DecideProjectLSPConsentsFor`。
3. `handleProjectMCPPreview` **只改带 `page` 的分支**：
   - 取 `declined := process.DeclinedProjectMCPConsents(workspace, launch)`。
   - 把 `mcpPendingPreviewRows(pending, launch)` 改成通用的 `mcpConsentPreviewRows(items []mcp.PendingProjectConsent, launch safety.ProjectContext, flag string)`：原来写 `row["pending"] = true` 的那一行改成 `row[flag] = true`，其余照旧（`scope=project`、`running=false`、`summary`）。待确认行调用时传 `"pending"`，已拒绝行传 `"declined"`。函数注释同步改。
   - 行的顺序改为（原型的项目层顺序）：
     1. 待确认；
     2. 项目层已同意（原循环里 `scope == project` 的部分）；
     3. **已拒绝**；
     4. 主代理层；
     5. 全局层。
     实现上把原来遍历三层的循环拆成：先写项目层，接着写已拒绝行，再遍历主代理与全局两层。
   - 表下说明不要重复表里已有的行。这个分支里的 `extra["not_applied"]` 改成**去掉名字出现在 `pending` 或 `declined` 里的条目**，名字比较用小写去空白；`pending_consent` 等其它键不变。
   - **不带 `page` 的分支一个字节都不改。**
4. `handleMCPLayerList`（`mcp_write_api.go`）：`scope == global` 且 `runtimeRunner != nil` 时：
   - 先用 `lc.Agents.MCPServersFor(runtimeRunner.AgentName)` 建小写名字集合，写法照 `handleMCPAgentView` 的 `shadowed`；
   - 渲染每个全局行后，名字在集合里的行加两个字段：`row["shadowed_by"] = appcfg.MCPServerScopeAgent`（即 `"agent"`）与 `row["shadowed_by_name"] = runtimeRunner.AgentName`。
   - 主代理层（`scope == agent`）不加。
5. 测试（`pkg/gateway/api_extra_test.go`）。
   - `TestProjectMCPConsentDecideOnlyTouchesNamedEntries`：
     - 用 `newMCPPreviewFixture`，并在 `f.root` 的 `.forebrain/mcp_servers.yaml` 末尾追加一条 `- name: f`（`/bin/project-f`）。
     - POST `/api/v1/projects/{id}/mcp/consent`，body `{"allow": [], "decide": ["e"]}`，期望 200。
     - 带 page 读预览：
       - 行名顺序 `["f", "d", "e", "b", "a"]`（待确认 f → 已同意 d → 已拒绝 e → 主代理 b → 全局 a）；
       - `e` 行 `declined == true`，且没有 `pending` 键；
       - `pending_consent` 只有 `f`；
       - `not_applied` 里没有 `e` 与 `f`。
     - 再 POST `{"allow": ["e"], "decide": ["e"]}`：`e` 行 `allowed == true`，没有 `declined` 键，`f` 仍待确认。
     - 最后 POST `{"allow": [], "decide": []}`：`f` 仍待确认。
   - `TestProjectMCPConsentWithoutDecideKeepsDecidingAll`：fixture 原样；POST `{"allow": []}` 之后 `e` 是已拒绝行。这是终端与旧调用方的语义，必须保持。
   - 改 `TestProjectMCPPreviewOrdersProjectAgentGlobalAndIncludesPending`：fixture 没有已拒绝条目，原断言应仍成立，**不要改断言**。
   - `TestProjectLSPConsentDecideOnlyTouchesNamedEntries`：
     - 项目、信任与 LSP 文件的造法照 `TestProjectLSPPreviewAndConsentEndpoint`，两条 `first`、`second`；
     - `decide: ["first"]` 拒绝之后，带 page 的 `rows` 里 `first` 是 `denied`、`second` 是 `pending`；
     - 再 `{"allow": ["first"], "decide": ["first"]}`，`first` 变 `allowed`。
   - `TestMCPLayerListMarksSharedEntriesShadowedByAgent`：造法照 `TestMCPAgentViewHidesShadowedGlobalAndListsAgentFirst`（全局 a、b，主代理 b、c）；`?scope=global` 的 `b` 行 `shadowed_by == "agent"`、`shadowed_by_name == "main"`，`a` 行没有这两个键；`?scope=agent` 的行都没有。

**Verify**：
```
CGO_ENABLED=1 go test -tags fts5 ./pkg/gateway -run 'ProjectMCP|ProjectLSP|MCPLayer|MCPAgentView' -count=1
CGO_ENABLED=1 go test -tags fts5 ./pkg/gateway -run 'ConsentDecideOnly|ConsentWithoutDecide|ShadowedByAgent' -count=1 -v 2>&1 | grep -c '^--- PASS'
```
第一条 `ok`；第二条输出 `4`。`gofmt -l pkg` 无输出。

### Step 3：终端文案与注释——N 就是拒绝，去哪里撤回

1. `cmd/forebrain/mcp_consent.go`：
   - `ensureProjectMCPConsent` 的 `default` 分支改为打印 `  declined — it won't load; allow it later on the project page's MCP tab`。
   - `ensureProjectLSPConsent` 的 `default` 分支改为打印 `  declined — it won't apply; allow it later on the project page's LSP tab`。
   - 其它打印（标题行、`Run it? [y/N] `、`Allow it? [y/N] `、`confirmed — …`）**不变**。
2. 同文件的注释要写对行为：
   - 文件头 `// Startup confirmation for project-level MCP servers.` 那段里的 `Anything not confirmed here fails closed: it stays out of the session and /mcp shows it as awaiting confirmation.` 改成两句：
     - 答 N（或直接回车）的条目记为拒绝，此后不再询问，直到在网页项目页允许它或条目配置改变；
     - 输入中断而没有回答的条目不记录，下次启动再问。
   - `ensureProjectLSPConsent` 上方注释里的 `Anything not confirmed here stays out until it is confirmed on the project page or at the next start.` 按同样意思改。
3. `pkg/lsp/control.go` 已拒绝条目的 `Note` 改为 `"project entry declined; allow it on the project page's LSP tab, or it asks again when .forebrain/lsp_servers.yaml changes"`，并把 `pkg/lsp/control_test.go` 里断言的原文同步改掉。
4. `cmd/forebrain/interactive_test.go` `TestEnsureProjectLSPConsentPrompt` 的期望字符串改成新文案。
   同文件照它新增 `TestEnsureProjectMCPConsentPrompt`：
   - 项目里两条 MCP 条目，输入 `"y\nn\n"`；
   - 断言输出含 `  confirmed — it loads this session` 与 `  declined — it won't load; allow it later on the project page's MCP tab`；
   - 断言 `process.DeclinedProjectMCPConsents` 只含第二条。workspace 用 `process.ActiveAgentWorkspace()`，launch 用 `process.ResolveConsentProjectContext(home, project)`，与被测函数取法相同。
   - 项目、信任与 `.forebrain/mcp_servers.yaml` 的造法照 `TestEnsureProjectLSPConsentPrompt`。MCP 文件格式见 `pkg/process/mcp_test.go` 的 `writeProjectMCP`：`mcp_servers:` 下每条 `- name / transport: stdio / command`。

**Verify**：
- `grep -rn "skipped — confirm later" cmd pkg` 无输出；
- `CGO_ENABLED=1 go test -tags fts5 ./cmd/forebrain ./pkg/lsp -count=1` 两行 `ok`。

### Step 4：前端 API 与类型

`frontend/src/lib/api.ts`：
1. 两个同意方法加第三个可选参数 `decide?: string[]`。**只有传了才把 `decide` 放进 body**，不传时 body 仍是 `{ allow }`：
   ```ts
   projectMcpConsent(id: string, allow: string[], decide?: string[]) {
     const body: { allow: string[]; decide?: string[] } = decide ? { allow, decide } : { allow }
     return postJson<{ ok: boolean }, typeof body>(`/v1/projects/${encodeURIComponent(id)}/mcp/consent`, body)
   },
   ```
   `projectLspConsent` 同样改，返回类型保持 `{ ok: boolean; allowed: string[] }`。
2. `McpServerRow` 加三个字段，并带注释：
   - `declined?: boolean`：项目视图里，本项目已拒绝的条目；
   - `shadowedBy?: string`：全局层里，被哪一层的同名条目覆盖；
   - `shadowedByName?: string`：覆盖它的主代理名。
   `mcpRowFromWire` 照 `pending` 的写法加三行：
   ```ts
   if ('declined' in wire) row.declined = flag('declined')
   if ('shadowed_by' in wire) row.shadowedBy = text('shadowed_by')
   if ('shadowed_by_name' in wire) row.shadowedByName = text('shadowed_by_name')
   ```
3. `AgentRosterRow` 加 `parentSessionId?: string`，注释写明：子代理所在的那条对话，主代理行没有。这个类型走 axios 驼峰拦截器，`parent_session_id` 会自动变成 `parentSessionId`，不用手写映射。
4. `api.test.ts` 加一条：`projectMcpConsent('p', ['x'], ['x'])` 发出的 body 是 `{ allow: ['x'], decide: ['x'] }`，`projectMcpConsent('p', ['x'])` 的 body 里**没有** `decide` 键。mock 写法照文件里已有的 `postJson` 类用例。

**Verify**：`cd frontend && pnpm test -- src/lib/api.test.ts` 通过；`npx vue-tsc --noEmit -p tsconfig.json` exit 0。

### Step 5：`mcpRows.ts`——已拒绝、被覆盖、连接状态

1. `mcpStatus(row)` 的判定顺序改为下面这样，第 2、3、8、9、10 条是新增或改写：
   1. 待确认（不变）。
   2. **新** `row.declined === true && row.scope === 'project'` → `{ tone: 'gray', label: t('mcp.status.declined'), tip: t('mcp.status.declinedTip') }`。
   3. **新** `(row.shadowedBy ?? '') !== ''` → `{ tone: 'gray', label: t('mcp.status.shadowed'), tip: t('mcp.status.shadowedTip', { name: row.shadowedByName || '—' }) }`。
   4. `disabledByConfig`（不变）。
   5. `disabledNextSession`（不变）。
   6. 失败（不变）。
   7. 项目层 `allowed` → `已允许`（不变）。
   8. **新** `row.connStatus === 'connecting'` → `{ tone: 'warn', label: t('mcp.status.connecting'), tip }`。
   9. **改** `row.running === true` 且 `connStatus` 为空、未定义或 `'connected'` → `已连接`（带工具数，同现状）。没有运行时信息时后端不给 `conn_status`，这种行仍算已连接。
   10. 其余 → `未运行`。包括 `unknown`、`disconnected`、`cancelled`。
   函数上方的注释按新顺序重写，并写明连接状态的全部取值见 `pkg/mcp/registry.go`。
2. `locales/index.ts`，中英文各加下列键。**先 `grep -n "'键名'" frontend/src/locales/index.ts` 确认不存在**，已存在就只改值；对象里重复键会让 `vue-tsc` 报 TS1117。

   | 键 | 中文 | 英文 |
   |---|---|---|
   | `mcp.status.declined` | `已拒绝` | `Declined` |
   | `mcp.status.declinedTip` | `不会加载；点“允许”撤回` | `Not loaded; Allow takes it back` |
   | `mcp.status.shadowed` | `未运行 · 被覆盖` | `Not running · overridden` |
   | `mcp.status.shadowedTip` | `被主代理 {name} 覆盖，未运行` | `Overridden by primary agent {name}; not running` |
   | `mcp.shadowedHint` | `被主代理 {name} 覆盖` | `Overridden by primary agent {name}` |
   | `mcp.status.connecting` | `连接中` | `Connecting` |
   | `projects.lspDeniedTip` | `不会启用；点“允许”撤回` | `Not applied; Allow takes it back` |

   **删除** `mcp.consentAllTip`（中英文两处），删除前 `grep -rn consentAllTip frontend/src` 确认只剩 `McpServersTable.vue` 一处使用（Step 6 会删掉它）。
3. `mcpRows.test.ts` 补用例：
   - 已拒绝的项目行 → `已拒绝`、灰、提示是 `declinedTip`；
   - 已拒绝但 `scope` 不是 project → 不判已拒绝；
   - `shadowedBy: 'agent', shadowedByName: 'main'` → `未运行 · 被覆盖`，提示含 `main`；
   - `running: true, connStatus: 'connecting'` → `连接中`（warn）；
   - `running: true` 且 `connStatus` 为 `disconnected`、`cancelled`、`unknown` → `未运行`（三例）；
   - `running: true` 不带 `connStatus` → `已连接`；
   - 原有用例保持通过。

**Verify**：`cd frontend && pnpm test -- src/components/mcp/mcpRows.test.ts` 通过。

### Step 6：MCP 表格与抽屉

`frontend/src/components/mcp/McpServersTable.vue`：
1. `TableRow` 加 `declined: boolean`，取值 `row.declined === true && row.scope === 'project'`，并写注释“本项目已拒绝的条目：只有‘允许’，没有开关”。
2. `tip` 改成 `status.tip`。删掉 `consentAllTip` 及其注释（`// A row awaiting a decision has nothing else to say, and its buttons decide every other pending entry too …` 那几行）。
3. 启用格：`v-else-if="!entry.pending"` 改为 `v-else-if="!entry.pending && !entry.declined"`，模板注释同步写上已拒绝。
4. 操作栏：在 `<template v-if="entry.pending">…</template>` 后面新增一段，结构照 pending 的 `允许` 按钮：
   ```vue
   <template v-else-if="entry.declined">
     <RowAction :icon="Check" :label="t('projects.mcpAllow')" :name="entry.row.name" tone="ok"
       :disabled="busy === entry.key" :data-testid="`mcp-undecline-${entry.key}`" @click="decide(entry.row, true)" />
     <RowActionSlot />
   </template>
   ```
   若文件还没有导入 `RowActionSlot`，照 `ProjectLsp.vue` 的导入加上。
5. `decide(row, allow)` 的调用改为 `forebrainApi.projectMcpConsent(projectId, allow ? [row.name] : [], [row.name])`。函数注释改成：只记录这一条的决定（`decide`），也用于撤回已拒绝的条目。

`frontend/src/components/mcp/McpServerDrawer.vue`，状态框，按原型 `row.over ?` 的写法：
6. `row.shadowedBy` 非空时，徽标文字用 `t('mcp.status.idle')`（`未运行`，灰），后面加 `<span class="mcp-key" data-testid="mcp-state-shadowed">{{ t('mcp.shadowedHint', { name: row.shadowedByName || '—' }) }}</span>`；其余情况不变。

测试：
7. `McpServersTable.test.ts`：
   - 原 `toHaveBeenCalledWith('proj-1', ['e'])` 改为 `('proj-1', ['e'], ['e'])`；
   - 拒绝改为 `('proj-1', [], ['e'])`（原文件没有拒绝的断言就补一条）；
   - 新增已拒绝行（`declined: true, scope: 'project'`）的用例：
     - 没有 `mcp-enable-…`、没有 `mcp-decline-…`；
     - 有 `mcp-undecline-project:x`；
     - 点击后调用 `('proj-1', ['x'], ['x'])`；
     - 徽标是 `已拒绝`。
   - 待确认行的徽标没有提示（`consentAllTip` 已删）。
8. `McpServerDrawer.test.ts`：被覆盖的行打开查看时，状态徽标是 `未运行`，并且有 `mcp-state-shadowed`，文字含 `main`。

**Verify**：`cd frontend && pnpm test -- src/components/mcp` 通过；`grep -rn "consentAllTip" frontend/src` 无输出。

### Step 7：项目 LSP 标签

`frontend/src/components/project/ProjectLsp.vue`：
1. `row.status === 'denied'` 的行，操作栏改为 `允许`（`Check`，`tone="ok"`，`:data-testid="\`lsp-undecline-${row.id}\`"`，点击 `consent(row.id, true)`）加一个 `<RowActionSlot />`；已允许的行保持两个空位。
2. 已拒绝的徽标加 `v-tip:top="row.status === 'denied' ? t('projects.lspDeniedTip') : ''"`。
3. `consent(id, allow)` 改为 `forebrainApi.projectLspConsent(props.projectId, allow ? [id] : [], [id])`。上方注释改成：每次只决定这一条，也用于撤回已拒绝的条目。
4. `ProjectLsp.test.ts`：
   - 原来约 106 / 115 行的断言改为 `('p1', ['projectls'], ['projectls'])` / `('p1', [], ['projectls'])`；
   - 新增用例：`denied` 行有 `lsp-undecline-…`，点击后调用 `('p1', ['x'], ['x'])` 并重新加载。

**Verify**：`cd frontend && pnpm test -- src/components/project/ProjectLsp.test.ts` 通过。

### Step 8：子代理“所在会话”指向父对话（073 Step 7.2 未执行部分）

1. `pkg/config/primary_agent.go` 的 `RosterRow` 在 `SessionID` 后加一行：
   ```go
   ParentSessionID string `json:"parent_session_id,omitempty"`
   ```
   字段对齐交给 `gofmt`。
2. `pkg/gateway/agents_api.go` `subagentRosterRow`：加 `ParentSessionID: strings.TrimSpace(entry.SessionID),`，`SessionID` 仍取 `entry.WorkerSessionID`，**不变**。
3. `handleAgentRoster` 里取标题的会话 id 改为：
   ```go
   titleSession := strings.TrimSpace(record.ParentSessionID)
   if titleSession == "" {
       titleSession = record.SessionID
   }
   ```
   然后用 `s.Sessions.SessionTitle(r.Context(), titleSession)`。主代理行没有父会话，仍按自己的会话取标题。
4. `pkg/gateway/agents_api_test.go` `TestAgentRosterPagesRunningRowsWithSessionTitles` 补断言：子代理行的 `parent_session_id` 是父对话 id，`session_title` 是**父对话**的标题。如果现有造法里父对话与 worker 会话标题相同，就给两者设不同的标题再断言。
5. `frontend/src/composables/useAgentRoster.ts` **新增**下面的函数，`agentRosterViewTarget` 不动：
   ```ts
   /** The conversation a row ran in: a subagent's parent conversation, or the
    * row's own session for a primary agent. */
   export function agentRosterConversationTarget(row: AgentRosterRow): RouteLocationRaw | null {
     const sessionId = String(row.parentSessionId ?? row.sessionId ?? '').trim()
     if (!sessionId) return null
     return { path: '/', query: { session: sessionId } }
   }
   ```
   `useAgentRoster.test.ts` 加两例：有 `parentSessionId` 时用它；没有时退回 `sessionId`。
6. `frontend/src/views/SubagentsView.vue`，“所在会话”那一格：
   - `v-if="sessionTarget(row)"` 改为 `v-if="conversationTarget(row)"`；
   - 点击改为 `jumpToConversation(row)`，即 `router.push(agentRosterConversationTarget(row))`；
   - 文字改为 `{{ row.sessionTitle || row.parentSessionId || row.sessionId }}`。
   操作栏的 `打开会话`（`MessageSquare`）**保持**原来的 `jumpToSession(row)`，即 worker 会话。`SubagentsView.test.ts` 补断言：点标题跳到 `?session=<parentSessionId>`，点 `打开会话` 跳到 `?session=<sessionId>`。

**Verify**：
- `CGO_ENABLED=1 go test -tags fts5 ./pkg/gateway -run 'Roster' -count=1` 结果 `ok`；
- `cd frontend && pnpm test -- src/views/SubagentsView.test.ts src/composables/useAgentRoster.test.ts` 通过；
- `gofmt -l pkg` 无输出。

### Step 9：e2e

`frontend/e2e/mcp-pages.spec.ts`：
1. 在 `'settings · MCP lists the shared layer only, …'` 用例里补断言：`global:b`（被主代理 b 覆盖）这一行的徽标是 `未运行 · 被覆盖`。行定位照文件里已有的 `[data-mcp-row="…"]` 写法。
2. 新增用例 `'deciding one project entry leaves the others pending, and a declined entry can be allowed again'`，结构照 `'a project’s MCP tab lists project, agent and shared, and decides a pending entry'`（含 `try/finally` 清理）：
   - 种数据：
     - `seedSharedLayers`、`seedProject(page, 'mcp-077')`、`seedProjectLayers`；
     - 再 `createEntry(page, projectPath(projectId), stdioEntry('f'))`，f 与 e 都待确认。
   - 打开 `/projects/{id}/mcp`，点 `mcp-decline-project:e`，然后断言：
     - `project:e` 的徽标轮询到 `已拒绝`；
     - `mcp-undecline-project:e` 可见；
     - 没有 `mcp-enable-project:e`；
     - **`project:f` 仍是 `待确认`**，它的 `mcp-allow-project:f` 可见；
     - 行顺序是 `['project:f', 'project:d', 'project:e', 'agent:b', 'agent:c', 'global:a']`。
   - 点 `mcp-undecline-project:e`，断言 `project:e` 徽标轮询到 `已允许`、出现 `mcp-edit-project:e`，`project:f` 仍是 `待确认`。
   - 截图 `077-project-mcp-declined.png`，放在与现有截图同一目录（文件里的 `shots` 变量）。

**Verify**：
- `FOREBRAIN_E2E_SPEC=e2e/mcp-pages.spec.ts scripts/acceptance/web_e2e.sh` 末行 `web e2e: PASS`；
- 然后跑全套 `scripts/acceptance/web_e2e.sh`，末行 `web e2e: PASS`。

## Test plan

- Go：
  - Step 1 的 6 个新测试（process 4 个、lsp 2 个）；
  - Step 2 的 4 个新测试；
  - Step 3 新增的 `TestEnsureProjectMCPConsentPrompt` 与改过文案的 `TestEnsureProjectLSPConsentPrompt`；
  - `pkg/lsp/control_test.go` 文案断言；
  - Step 8 的花名册断言。
- 前端单测：`api.test.ts`、`mcpRows.test.ts`、`McpServersTable.test.ts`、`McpServerDrawer.test.ts`、`ProjectLsp.test.ts`、`useAgentRoster.test.ts`、`SubagentsView.test.ts`。
- e2e：`mcp-pages.spec.ts` 的新用例与补充断言，然后全套。
- **不需要**真模型验收：本计划不涉及模型请求。

## Done criteria

- [ ] Drift check 在开工前全部有输出（记在报告里）
- [ ] `CGO_ENABLED=1 go test -tags fts5 ./pkg/process ./pkg/lsp ./pkg/gateway ./pkg/config ./cmd/forebrain -count=1` 每行 `ok`；`go vet`（同上包）与 `gofmt -l pkg cmd` 无输出
- [ ] `grep -rn "skipped — confirm later" cmd pkg` 无输出；`grep -rn "consentAllTip" frontend/src` 无输出
- [ ] `grep -nF 'decide' pkg/gateway/mcp_oauth_api.go pkg/gateway/api_extra.go` 两个文件都有输出
- [ ] `cd frontend && pnpm test` 全绿；`npx vue-tsc --noEmit -p tsconfig.json` exit 0
- [ ] `FOREBRAIN_E2E_SPEC=e2e/mcp-pages.spec.ts scripts/acceptance/web_e2e.sh` 与全套 `scripts/acceptance/web_e2e.sh` 末行都是 `web e2e: PASS`
- [ ] `git status --short | diff $TMPDIR/077-before.txt -` 里新出现或变化的路径都在 Scope 内，逐个核对后写进报告

## STOP conditions

- Drift check 任一条没有输出（代码已经变了）。
- `mcp.LoadProjectConsents` / `lsp.LoadProjectConsents` 的签名或存储结构与 “Current state” 不符。
- Step 2 改完后，`TestProjectMCPPreviewOrdersProjectAgentGlobalAndIncludesPending` 或 `TestProjectMCPPreviewWithoutPageKeepsOldFields` 失败。这说明动到了不该动的排序或不带 page 的分支。先报告，**不要改这两个测试的断言**。
- 有调用方依赖“同意接口不带 `decide` 时一次决定全部”之外的行为（例如 e2e 种数据开始失败）。
- `agent.HistoryEntry.SessionID` 实际不是父对话 id（例如 Step 8 的测试里父对话与它对不上）。报告，不要改历史记录的写入方。
- 要改 Scope 以外的文件才能通过测试。

## Maintenance notes

- **两种同意语义并存**：
  - 终端启动提示问的是“这一批”，不带 `decide`：没答 y 的全记拒绝，已决定的跳过；
  - 网页问的是“这一条”，带 `decide`：只改点名的条目，可以改写已有决定。
  以后新增确认入口（比如 TUI 里的面板）时，按“问的是一批还是一条”选函数。
- 项目 MCP 表格行里，`pending` 有两种含义：项目层未同意的行，以及主代理/全局行的“这一代 MCP 启动未完成”。前端靠 `scope === 'project' && allowed !== true` 区分。“已拒绝”用独立的 `declined` 键，不要再往 `pending` 上叠含义。
- `shadowed_by` 目前只标“被主代理覆盖”，因为设置页只看全局层，主 runner 也没有项目层。若以后设置页要在项目会话的上下文里显示，再加 `"project"` 的取值，前端提示要跟着分开写。
- 项目 LSP“已允许”的行没有撤回入口（owner 没裁定）。如果要加，先改原型给 owner 确认。
- 终端文案指向“项目页的 MCP / LSP 标签”。项目页标签改名时，要同步改 `cmd/forebrain/mcp_consent.go` 与 `pkg/lsp/control.go` 的这两句。
