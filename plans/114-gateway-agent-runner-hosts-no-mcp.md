# Plan 114：网关也只让项目 runner 起 MCP——基础 runner 不再启动任何服务；设置 / 主代理 MCP 页只显示配置状态，项目 MCP 标签读该项目 runner 的实况

> **执行者须知**：按步骤顺序执行，每步跑验证命令并确认预期结果后再进下一步。触发
> "STOP conditions" 时停下报告，不要即兴发挥。**不要更新 `plans/README.md`**（评审方维护索引）。
> **禁止提交代码**（owner 手动提交）：不 commit、不 push、不开 PR、不建分支、不用 worktree，改动直接留在 dev 工作区。
> `pkg/gateway/*`、`frontend/*`、`docs/plan/WEB_REDESIGN_AND_GATEWAY_ACCESS/app-preview.html` **已有 owner 未提交的改动**：只做本计划的修改。
>
> **owner 2026-10-24 裁决：不做原型，直接开发落地。** Part A 不执行，不改 `app-preview.html`；Part A 各步骤对界面（布局、状态、文案）的描述就是实现规格，
> Part B 里“按 / 1:1 还原 Part A 定稿”一律理解为“按 Part A 各步的描述实现”。直接从 Part B 开始。
>
> **前置**：`plans/111` 已落地：`grep -c 'AgentRunnerWithoutMCP bool' pkg/process/open.go` → `1`，`grep -c 'AgentRunnerWithoutMCP: true' pkg/tui/notify.go` → `1`。否则 STOP 1。

## Status

- **Priority**: P2
- **Effort**: M
- **Risk**: MED（网关里所有 MCP 页面的状态来源变化）
- **Depends on**: `plans/111-tui-conversations-run-on-project-runners.md`
- **Category**: bug + direction（owner 2026-10-23：层级 **会话 → 项目 → 主代理 → 全局**，所有会话建立在项目之上）
- **Planned at**: commit `f18ff14`（dev 工作区含 owner 未提交改动），2026-10-23

## Why this matters

网关（`forebrain gateway start`）的每个会话都跑在所属项目的 runner 上（`RunnerForTurn`；无项目的会话直接拒绝，见 R4），可网关的**基础 runner**
（`process.Environment.Runner`）启动时照样起全局 + 主代理那一组 MCP 服务——它不承载任何会话，这组进程是纯粹的**第二份**：
每个在跑的项目 runner 又各起一份全局服务。它唯一的用处是给「设置 · MCP」「主代理 MCP」页的状态列提供「已连接 · N 个工具」，而那个状态并不是任何会话看到的状态。

owner 裁定（2026-10-23）：基础 runner 一个 MCP 都不起；**设置 / 主代理 MCP 页只显示配置状态**（已启用 / 已禁用 / 配置里已禁用 / 被覆盖）；
「已连接 · N 个工具 / 连接失败」只在会话工作台和项目 MCP 标签里显示，读该项目 runner 的实况，项目没在跑就显示「未启动」。
`111` 已在 TUI 里用过渡开关 `OpenOptions.AgentRunnerWithoutMCP` 做到这一点；本计划让网关也打开它，然后**删除这个开关**，使之成为唯一行为。

## Current state

- 网关开环境：`pkg/gateway/serve_run.go:35-47`（`process.Open(ctx, process.OpenOptions{..., NoLaunchProject: true})`）。
- 开关（`111` 加的）：`pkg/process/open.go` 的 `OpenOptions.AgentRunnerWithoutMCP`，置位时不解析基础 runner 的 MCP、`MCPResolve` / `MCPDiagnostics` 置 nil；TUI 在 `pkg/tui/notify.go` 的 `openProcessChatSession` 里置位。
- 读基础 runner MCP 的网关代码：
  - `pkg/gateway/mcp_oauth_api.go:55-75` `findMCPServerMerged`：OAuth 两个处理器（`:215`、`:279`）按名字找服务配置，**列表取自 `s.Runner.MCPServers` + `s.Runner.MCPDisabled`**——基础 runner 不再有列表后这里会找不到。
  - `pkg/gateway/mcp_oauth_api.go:105-135` `handleMCPServersV1`：带 `session_id` 用该会话的 runner，否则用 `s.Runner`（`runtime_scope: "primary"`）。
  - `pkg/gateway/mcp_oauth_api.go:402` 项目 MCP 预览（项目页 MCP 标签）：`rc := s.mcpRowsContext(lc, s.Runner)`——**用基础 runner 的实况给项目页的行上状态**，本身就是错的。
  - `pkg/gateway/mcp_write_api.go:880-905`（`handleMCPLayerList`）、`:925-935`（`handleMCPAgentView`）：只用 `s.Runner.AgentName` 取主代理名——不受影响。
- 池：`pkg/process/runner_pool.go:1140` `RunnersForProject(projectID) []*run.Runner`（一个项目可能有多代 runner：项目设置改过后新会话用新一代）。
- 前端状态徽标：`frontend/src/components/mcp/mcpRows.ts:79` `mcpStatus(row)`，优先级：待确认 → 已拒绝 → 被覆盖 → 配置里已禁用 → 已禁用（下个会话起）→ 连接失败 → 已允许 → 连接中 → 已连接 → 未运行。
  表格 `frontend/src/components/mcp/McpServersTable.vue`；设置页、主代理页、项目页、会话工作台都用它。
- 设计稿：`app-preview.html` 的 `mcpp`（设置 · MCP）、`mcpagent`（MCP 页 · 全局 + 主代理）、`mcpproj`（项目 · MCP 三层）状态；约 `:1401` 与 `:2942` 的状态词说明。

## Commands you will need

| Purpose | Command | Expected |
|---|---|---|
| 构建 | `go build ./...` | exit 0 |
| 后端测试 | `go test -tags fts5 ./pkg/gateway/ ./pkg/process/ ./pkg/tui/` | `ok` ×3 |
| 护栏 | `go test ./pkg/architecture/` | `ok` |
| 前端 | `cd frontend && pnpm typecheck && npx vitest run src/components/mcp src/components/project src/components/workbench && pnpm build` | 全部通过 |
| 格式 | `gofmt -l pkg/gateway pkg/process pkg/tui` | 无输出 |

## Scope

**Part A**：不执行（只作界面规格，不改任何文件）。

**Part B**：`pkg/gateway/serve_run.go`、`pkg/gateway/mcp_oauth_api.go`、`pkg/gateway/*_test.go`（相关用例）、`pkg/process/open.go`、`pkg/process/runner_pool.go`、
`pkg/process/*_test.go`（相关用例）、`pkg/tui/notify.go`（删一行）、`frontend/src/components/mcp/mcpRows.ts`、`McpServersTable.vue`、调用它的设置 / 主代理 / 项目页组件、`frontend/src/locales/index.ts`、相关前端测试、
`pkg/architecture/testdata/graph.json`（只由脚本重新生成）。

**Out of scope**：会话工作台的 MCP 卡片（已经读会话的 runner）；项目确认（`113`）；TUI（`111`/`112`）。

## Part A：界面规格（不改原型；下面列出的状态词与页面行为即 Part B 的实现依据，不做“更新原型/截图”的动作）

### Step A1

1. `mcpp` 与 `mcpagent`：状态列只剩配置状态徽标：`已启用`（新）、`已禁用`、`配置里已禁用`、`未运行 · 被覆盖`；去掉「已连接 · N 个工具 / 连接失败 / 连接中」。悬停提示不加新句子。
2. `mcpproj`：全局 / 主代理 / 项目三层的行状态来自该项目 runner；加一个状态展示「项目没在跑」时各行显示 `未启动`。
3. 更新约 `:1401` 的状态词说明与约 `:2942` 的页面描述（各一句，简洁）。


## Part B：实现

### Step B1：网关打开开关，补上按名字找配置

1. `serve_run.go`：`process.OpenOptions{...}` 加 `AgentRunnerWithoutMCP: true`。
2. `findMCPServerMerged`：不再读 `s.Runner.MCPServers`。改为用 `process.ResolveSessionMCP(s.Home, workspace, lc.Agents, agentName, safety.ProjectContext{})`
   （`workspace`/`agentName` 取 `s.Runner.WorkspaceRoot` / `s.Runner.AgentName`；空的 launch 即「全局 + 主代理」两层，与网关基础 runner 原来持有的列表相同）的 `Servers` + `Disabled`；
   `s.Runner == nil` 时保持原来的 `lc.Agents.Defaults.MCPServers` 回退。
3. `handleMCPServersV1`：没有 `session_id` 时不读任何 runner 的实况（`runtime` 为 nil，`runtime_scope` 回 `"none"`）；有 `session_id` 但解析到基础 runner 时同样当作 `"none"`。
4. 项目 MCP 预览（`:402`）：实况改取该项目的 runner——在 `runner_pool.go` 加
   `func (p *RunnerPool) LatestRunnerForProject(projectID string) (*run.Runner, bool)`（返回该项目 `lastUsed` 最新的条目的 runner，不建新的、不改 `lastUsed`），
   有就 `s.mcpRowsContext(lc, r)`，没有就 `s.mcpRowsContext(lc, nil)`（行上没有实况 ⇒ 前端显示「未启动」）。

**Verify**：`go build ./... && go test -tags fts5 ./pkg/gateway/ ./pkg/process/` → `ok`。

### Step B2：前端徽标

`mcpRows.ts`：`mcpStatus(row, opts?: { configOnly?: boolean })`——`configOnly` 时在「配置里已禁用 / 已禁用」之后直接返回 `已启用`（新文案键 `mcp.status.enabled`，中英）；
项目页在后端没有实况时（行上无 `connStatus` 且 `running` 不为 true）显示 `未启动`（新键 `mcp.status.notStarted`）。设置页与主代理页传 `configOnly: true`。按 Part A 定稿 1:1 还原。

### Step B3：删除过渡开关

1. `process/open.go`：删 `OpenOptions.AgentRunnerWithoutMCP` 字段，把它控制的行为改成无条件（基础 runner 永不解析、不持有 MCP 列表、`MCPResolve` / `MCPDiagnostics` 为 nil），注释改为陈述句（不再提「过渡」「plans/114」）。
2. 删 `serve_run.go` 与 `pkg/tui/notify.go` 里的 `AgentRunnerWithoutMCP: true`。
3. `grep -rn 'AgentRunnerWithoutMCP' .` → 无输出。
4. 跑 `go test -tags fts5 ./pkg/process/ ./pkg/gateway/ ./pkg/tui/`：若有既有测试断言「基础 runner 持有/启动 MCP」，逐个判断——测的是旧行为就改为断言项目 runner；
   不确定的列出来 STOP 3。

**Verify**：`go build ./... && go test -tags fts5 ./pkg/gateway/ ./pkg/process/ ./pkg/tui/` → `ok` ×3；`go test ./pkg/architecture/` → `ok`。

### Step B4：真机验收

隔离 home，全局配置两个 stdio MCP（`/bin/sleep 71`、`/bin/sleep 72`），`forebrain gateway start`：

1. 刚启动、没有会话：`pgrep -f '/bin/sleep 7[12]' | wc -l` → **0**（修复前 2）。
2. 设置 · MCP 页：两行都是 `已启用`，没有连接状态。
3. 在一个项目里新建会话并发一句话：→ 2；项目 MCP 标签显示这两行的实况；再开第二个项目的会话 → 4（每个项目一份，没有基础 runner 那一份）。
4. MCP OAuth 入口（若配置了需要 OAuth 的远程服务）仍能找到服务配置（`findMCPServerMerged` 不报 not found）。
5. 停网关 → 0。

截图与数字写进报告。

## Test plan

- `pkg/gateway`：`findMCPServerMerged` 在基础 runner 无列表时仍按名字找到全局与主代理的条目（含被禁用的）；`handleMCPServersV1` 无 `session_id` 时 `runtime_scope == "none"`；
  项目预览在项目有 runner 时行上带实况、没有时不带。
- `pkg/process`：`LatestRunnerForProject` 选最新一代、不建新条目；`Open`（无任何选项）后 `env.Runner.MCPServers` 为空且 `MCPResolve == nil`。
- 前端：`mcpRows` 的 `configOnly` 分支与 `未启动`；设置页 / 主代理页组件传了 `configOnly`。

## Done criteria

- [ ] `grep -rn 'AgentRunnerWithoutMCP' .` 无输出
- [ ] 后端、前端命令全部通过；`gofmt` 无输出；包图只有 `loc` 变化
- [ ] Step B4 五项通过（刚启动 0 个服务进程）

## STOP conditions

1. 前置（`111`）未落地。
2. 删除开关后，有既有测试测的是「基础 runner 起 MCP」且无法判断是否只是旧行为。
3. 发现网关里还有别的功能真正依赖基础 runner 的 MCP 连接（例如某个不属于任何会话的后台任务要调 MCP 工具）——列出调用点停下，需要 owner 决定它归属哪个项目。

## Maintenance notes

- 规则：**任何 MCP 运行状态都来自某个会话所属项目的 runner**；主代理 / 全局层只有配置。新增 MCP 相关页面或接口时照此取数。
- 一个项目可能同时有多代 runner（项目设置改过、旧会话仍在旧代上）；项目页只展示最新一代，这是有意的简化。
