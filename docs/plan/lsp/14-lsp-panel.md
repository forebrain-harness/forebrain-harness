# 任务 14：`/lsp` 面板、REST 接口、Web 页面与 `/status` 行

> **执行者须知**：逐步执行；每一步运行「验证」命令并确认结果后再进入下一步。出现「STOP 条件」中的任何情况立即停止并汇报。完成后把 `docs/plan/lsp/README.md` 中本任务状态改为 `DONE`。
>
> **规范**：`docs/plan/LSP_CODE_INTELLIGENCE_PLAN.md`。开始前读规范 §5.4、§6.5、§9.6、§11.1、§11.3、§15 第 4、10 条。
>
> **前置**：任务 12 已合入（`Manager.Install` 与快照里的安装状态可用）。先确认：`grep -n "Installing" pkg/lsp/control.go` 有输出，否则 STOP。
>
> **漂移检查（先运行）**：`git diff --stat 6305ea9..HEAD -- pkg/turn/slash.go pkg/turn/executor.go pkg/turn/model.go pkg/turn/mcp_format.go pkg/tui/run.go pkg/tui/render.go pkg/tui/commands.go pkg/tui/chat_slash.go pkg/tui/chat_session.go pkg/tui/notify.go pkg/gateway/api_extra.go pkg/gateway/slash_handlers.go pkg/gateway/server.go frontend/src/router/index.ts frontend/src/App.vue frontend/src/lib/api.ts frontend/src/locales/index.ts`。任务 10、11 改过 `render.go`；对比「现状」摘录。

## 状态

- 优先级：P2 · 工作量：L · 风险：LOW（只加界面；不改模型可见内容）
- 依赖：12
- 类别：direction
- 计划基于：`6305ea9`，2026-09-29

## 为什么

用户需要一个地方看到每个语言服务器的状态（是否安装、是否启用、是否在运行、在索引还是出错），并能启用、停用、重启、安装、打开日志、重新开启推荐。终端与 Web 用同一份数据（`tool.CodeIntelControl.Snapshot()`），文字版渲染共用一个函数。

## 现状

- **命令表**：`pkg/turn/slash.go:73 var commands`，`/mcp` 一行在 :91；`commandCategory`（:232 `case "skills", "mcp", "sandbox": return "tools"`）；`defaultActionKind`（:243 的列表含 `"mcp"` → `"open-panel"`）。
- **执行**：`pkg/turn/executor.go:83 case "mcp": return execMCP(ctx)`；`execMCP`（:674）调用 `ctx.MCP.HandleMCPSlash(ctx.SessionID, ctx.Channel)`。`turn.Context` 的处理器字段在 `pkg/turn/model.go:395-417`（`MCP MCPSlashHandler` 在 :411）；`MCPSlashHandler` 接口在 :194。
- **构造 `turn.Context` 的两处**：终端 `pkg/tui/chat_session.go:1310`（`MCP: s`）、gateway `pkg/gateway/server.go:2149`（`MCP: s`）。
- **文字渲染**：`pkg/turn/mcp_format.go:541 RenderMCPInventoryMarkdown`、`:506 MCPServerStatusLabel`（`✓ connected`、`✗ failed`、`○ idle` 等字形）、`:50 MCPCountLine.Render`（`"2 connected, 1 failed"`，全零时为空）。
- **`/status`**：`turn.StatusSource`（`pkg/turn/model.go:662`）、`turn.StatusReport`（:624）、`BuildStatusReport`（:704）、`StatusFacts`（:880，MCP 行：`add("MCP servers", counts+" · /mcp")`）。两个构造点：终端 `pkg/tui/chat_slash.go:97 statusSource`、gateway `pkg/gateway/slash_handlers.go:92`。
- **终端面板**：
  - `pkg/tui/run.go:4427 type uiPanel struct { kind string // "status" | "mcp" … }`；`:4467 panelSession` 接口；`:4497 openMCPPanel`；`:4515 closeUIPanel`（调用 `cancelMCP`）；`handleUIPanelKey`（:4541）、`back()`（:4579）、`panelAccept`（:4623）、`panelSelectableRows`（:4700 附近）都以 `panel.kind == "mcp"` 分支。
  - `cmds.openPanel`（`run.go:200-215`）按 `kind` 打开面板；`processUINotification` 里 `MCPStatusTickMsg`（:251）在面板打开时刷新并重画。
  - `pkg/tui/render.go:7370 buildUIPanel` 按 `kind` 分派；`buildMCPPanel`（:7557）、`buildMCPList`、`buildMCPDetail` 用 `panelBuilder` 的 `title/subtitle/group/selectable/facts/text/blank/hint`（:7397-7500）。
  - `pkg/tui/commands.go:1596 handleMCP`：先 `c.openPanel("mcp")`，失败（非视口模式）时渲染 `HandleMCPSlash` 的文字；`run.go:1142` 的 slash 分派 `case "mcp": _ = cmds.handleMCP(state.sessionID)`。
  - 会话方法：`pkg/tui/chat_session.go:2422` 起的 `Panel*`；`pkg/tui/chat_slash.go:1171 HandleMCPSlash`。
- **gateway**：路由表在 `pkg/gateway/api_extra.go:60-160`（`mcp := api.Group("/v1/mcp")` 在 :123）；会话 Runner 解析用 `s.runnerFor(ctx, sessionID)`，缺省 `s.Runner`（范例 `pkg/gateway/mcp_oauth_api.go:483 handleMCPServerToggle`）；`HandleMCPSlash` 在 `pkg/gateway/slash_handlers.go:142`。
- **Web**：`frontend/src/views/McpView.vue`（页面范例：`lastSessionIdValue()` 取会话、`forebrainApi` 取数、错误与加载状态）、`McpView.test.ts`；路由 `frontend/src/router/index.ts:32`；导航 `frontend/src/App.vue:209`（`{ to: '/mcp', label: L.mcp, icon: Plug }`）；导航文案 `frontend/src/locales/index.ts`（`nav.mcp` 在中文 :95、英文 :652；`navLabels` 在 :1187）；API 客户端 `frontend/src/lib/api.ts`（`mcpSetServerDisabled` 在 :414）。
- 结构约束：`turn`、`tui`、`gateway` 都已满 20 个生产文件，只能改已有文件；fan-out 已满，不得新增导入（`turn`、`tui`、`gateway` 都已导入 `event`）。

## 范围

**修改：**

- `pkg/turn/slash.go`、`executor.go`、`model.go`、`mcp_format.go` 及对应 `_test.go`
- `pkg/tui/run.go`、`render.go`、`commands.go`、`chat_slash.go`、`chat_session.go`、`notify.go` 及对应 `_test.go`
- `pkg/gateway/api_extra.go`、`slash_handlers.go`、`server.go` 及对应 `_test.go`
- `frontend/src/router/index.ts`、`frontend/src/App.vue`、`frontend/src/lib/api.ts`、`frontend/src/locales/index.ts`
- `pkg/architecture/testdata/graph.json`

**新建：** `frontend/src/views/LspView.vue`、`frontend/src/views/LspView.test.ts`。

**不要碰：** `pkg/lsp`（只调用 `CodeIntelControl`）、`pkg/run`、`pkg/gateway/dist`。

## 步骤

### 步骤 1：共用的文字渲染（`pkg/turn/mcp_format.go`）

```go
// LSPServerStateLabel is one language server's state for the panels and the
// text reply, glyph first like MCPServerStatusLabel.
func LSPServerStateLabel(s event.LSPServerStatus) string

// LSPStatusLine is the /status row: counts of enabled servers by state, ""
// when no server is enabled (the row is then omitted).
func LSPStatusLine(snap event.LSPSnapshot) string

// RenderLSPInventoryMarkdown renders /lsp for the web chat and non-TTY replies.
func RenderLSPInventoryMarkdown(snap event.LSPSnapshot) string
```

`LSPServerStateLabel`（逐字）：

| State | 文本 |
|---|---|
| `ready` | `✓ ready` |
| `indexing` | `indexing… {IndexingPercent}%`（百分比为 0 时省略 ` {n}%`） |
| `starting` | `starting…` |
| `failed` | `✗ failed` |
| `stopped` | `○ enabled, not running` |
| `available` | `○ available` |
| `not_installed` | `– not installed` |
| `blocked` | `△ blocked` |

`Installing` 为 true 时整体改为 `installing…`。`Enabled` 且 `Errors+Warnings > 0` 时追加 ` · {Errors} errors, {Warnings} warnings`（为 0 的一项省略，单数不加 s）。

`LSPStatusLine`：只统计 `Enabled` 的服务器，按 `ready → "running"`、`starting`/`indexing → "indexing"`、`failed → "failed"`、`stopped → "idle"`、`not_installed → "not installed"`、`blocked → "blocked"` 归类，按此顺序输出非零项 `"{n} {label}"`，用 `", "` 连接。

`RenderLSPInventoryMarkdown`：

```
Language servers · {N} configured, {M} enabled
Project: {ProjectRoot}（未受信任时加 " (not trusted: language servers do not start here)"；无项目时整行为 "No project: language servers start only in trusted projects"）

Enabled
- {id} · {state label} · {Languages 用 ", " 连接}
  Error: {LastError}（有 LastError 时）
Available
- {id} · {state label} · {Languages}
```

- `FeatureEnabled == false` 时第二行后加一行 `Turned off by features.lsp`。
- `ProjectNotes` 的每一条在末尾各占一行：`Project file: <note>`（任务 15 之前该字段总是空；终端列表页的说明行与 `LspView.vue` 也照此显示）。
- `RecommendationsDisabled` 时末尾加空行与 `Recommendations are off ({reason}); /lsp can turn them back on`。
- 两组中为空的组省略；`Servers` 为空时只输出第一行改为 `No language servers configured`。

### 步骤 2：命令与 `/status`（`pkg/turn`）

- `slash.go`：在 `mcp` 一行之后加 `{Name: "lsp", Description: "language servers: status, enable, restart, diagnostics", AllowedSurfaces: []Surface{SurfaceWebChat, SurfaceTUI}, SupportsInlineArgs: false, Visibility: VisibilityPublic}`；`commandCategory` 的 `"skills", "mcp", "sandbox"` 分支加 `"lsp"`；`defaultActionKind` 的 open-panel 列表加 `"lsp"`。
- `model.go`：

  ```go
  // LSPSlashHandler answers /lsp, which takes no arguments.
  type LSPSlashHandler interface {
  	HandleLSPSlash(sessionID, channel string) (reply string, handled bool)
  }
  ```

  `Context` 在 `MCP` 之后加 `LSP LSPSlashHandler`；`StatusSource` 加 `LSP *event.LSPSnapshot // nil when the runtime has no language servers`；`StatusReport` 加 `LSP string`；`BuildStatusReport` 在 `src.LSP != nil` 时 `rep.LSP = LSPStatusLine(*src.LSP)`；`StatusFacts` 在 MCP 行之后加 `if rep.LSP != "" { add("Language servers", rep.LSP+" · /lsp") }`。
- `executor.go`：`case "lsp": return execLSP(ctx)`，`execLSP` 照 `execMCP`（`ctx.LSP == nil` → `"lsp: unavailable"`）。

**验证**：`CGO_ENABLED=1 go test -tags fts5 ./pkg/turn -count=1` → `ok`（命令表的已有测试若断言命令数量或顺序，按新增一行更新期望）。

### 步骤 3：终端会话方法（`chat_session.go`、`chat_slash.go`、`notify.go`）

`chat_session.go`，与 `Panel*` 放在一起：

```go
// The /lsp panel's slice of the session. Every call returns at once; an
// install runs in the background and reports through the snapshot.
func (s *ChatSession) PanelLSPSnapshot() (event.LSPSnapshot, error)          // errPanelUnavailable when the runner or its control is nil
func (s *ChatSession) PanelSetLSPEnabled(serverID string, enabled bool) (string, error)
func (s *ChatSession) PanelRestartLSP(serverID string) (string, error)
func (s *ChatSession) PanelInstallLSP(serverID string) (string, error)     // starts ctl.Install in a goroutine
func (s *ChatSession) PanelResetLSPRecommendations() (string, error)
func (s *ChatSession) SubscribeLSPStatus() (cancel func(), ok bool)        // ctl.Subscribe → s.notifyUI(LSPStatusTickMsg{})
```

返回的提示文本（逐字）：启用 `Enabled. Diagnostics start with the next edit; the lsp tool appears in new sessions.`；停用 `Disabled. Its instances in this project have stopped.`；重启 `Restarting…`；安装 `Installing: {InstallCommand}`；重置 `Recommendations are back on.`。

`notify.go`：加 `type LSPStatusTickMsg struct{}`（照 `MCPStatusTickMsg`）。

`chat_slash.go`：`HandleLSPSlash(sessionID, channel)` 照 `HandleMCPSlash`，返回 `turn.RenderLSPInventoryMarkdown(snapshot)`；`statusSource` 里 `if ctl := r.CodeIntelControl; ctl != nil { snap := ctl.Snapshot(); src.LSP = &snap }`（先构造 `src` 再赋值）。`chat_session.go:1310` 的 `turn.Context` 加 `LSP: s`。

### 步骤 4：终端面板（`run.go`、`render.go`、`commands.go`）

1. `run.go`：
   - `uiPanel.kind` 的注释改为 `"status" | "mcp" | "lsp"`；加字段 `lsp *event.LSPSnapshot`、`lspSession lspPanelSession`、`cancelLSP func()`；`page` 的注释补充 lsp 的 `"list"`、`"detail"`；`server` 复用为选中的服务器 id。
   - 新接口（不扩展 `panelSession`，避免所有测试替身都要实现）：

     ```go
     // lspPanelSession is what a session must expose for the /lsp panel.
     type lspPanelSession interface {
     	PanelLSPSnapshot() (event.LSPSnapshot, error)
     	PanelSetLSPEnabled(serverID string, enabled bool) (string, error)
     	PanelRestartLSP(serverID string) (string, error)
     	PanelInstallLSP(serverID string) (string, error)
     	PanelResetLSPRecommendations() (string, error)
     	SubscribeLSPStatus() (cancel func(), ok bool)
     }
     ```

   - `openLSPPanel(state)` 照 `openMCPPanel`：取快照；`uiPanel{kind: "lsp", page: "list", lsp: &snap, lspSession: ls, sessionID, top: true, notice: map[string]string{}}`；订阅 `SubscribeLSPStatus` 存入 `cancelLSP`。
   - `cmds.openPanel` 的 `switch kind` 加 `case "lsp": opened = openLSPPanel(&state)`。
   - `closeUIPanel`：`cancelLSP != nil` 时调用。
   - `processUINotification`：`case LSPStatusTickMsg:` 面板为 `lsp` 时重新取快照并 `drawUIPanel`。
   - `back()`：`kind == "lsp"` 时 `detail → list`（光标回到打开的那一行），`list` 返回 false。
   - `panelSelectableRows`：`kind == "lsp"` 分支：
     - `list`：每个服务器一行（顺序同快照：已启用在前）`{label: id, action: panelOpenDetail, value: id}`；`RecommendationsDisabled` 时末尾加 `{label: "Turn recommendations back on", action: panelLSPResetRecs}`。
     - `detail`：按顺序（只列适用的）：`Enable`（`!Enabled` 且状态不是 `blocked`）/`Disable`（`Enabled`）、`Restart`（`Enabled` 且状态为 `ready`、`indexing`、`starting`、`failed` 之一）、`Install: {InstallCommand}`（`not_installed`、有 `InstallCommand` 且不在安装中）。
   - 新增 `panelRowAction` 常量：`panelLSPEnable`、`panelLSPDisable`、`panelLSPRestart`、`panelLSPInstall`、`panelLSPResetRecs`；`panelAccept` 对 `kind == "lsp"` 执行对应的 `lspSession` 方法，把返回文本（或错误文本）存进 `panel.notice[id]`，然后刷新快照。
2. `render.go`：`buildUIPanel` 加 `case "lsp": buildLSPPanel(b, panel, spinner)`；

   - `mcpStatusText` 的着色写法可以直接照搬；如果它依赖 `turn.MCPServerEntry`，就写一个接收 `event.LSPServerStatus` 的同构函数 `lspStateText`，不要改 `mcpStatusText`。
   - 列表页：`b.title("Language servers")`；副标题：无项目 → `No project: language servers start only in trusted projects`；未受信任 → `{ProjectRoot} · not trusted, servers do not start here`；`FeatureEnabled == false` → `Turned off by features.lsp`；否则 `{ProjectRoot} · {n} enabled`。每行 `b.selectable(selected, "", id+" · "+状态文本)`，状态文本为 `turn.LSPServerStateLabel(s)`，字形着色照 `mcpStatusText`（`✓` 用 `panelOKStyle`，`✗` 用 `panelFailStyle`，`△` 用 `panelWarnStyle`，`starting…`/`indexing…`/`installing…` 前加 `spinner`，其他用 `panelGlyphStyle`）。分组：`b.group("Enabled")` 与 `b.group("Available")`。末尾说明行：`Servers run on this machine outside the sandbox, only in trusted projects.`；推荐关闭时 `Recommendations are off ({reason}).`。提示：`↑/↓ to navigate · Enter to open · Esc to close`。
   - 详情页：`b.title(id + " language server")`；`b.facts(...)`，标签与取值（空值省略）：`State`（状态文本）、`Languages`、`Scope`、`Command`、`Binary`（`BinaryPath` + 版本）、`Roots`（`, ` 连接）、`Processes`（PIDs）、`Open files`、`Problems`（`{Errors} errors, {Warnings} warnings`）、`Last error`、`Log`、`Writes into project`、`Note`；然后可选择的动作行；`notice[id]` 非空时空一行后显示它；`Installing` 或 `InstallError != ""` 时空一行后显示 `Install output` 小标题与 `InstallLog` 各行（`InstallError` 的首行放最前）。提示：`↑/↓ to navigate · Enter to run · Esc to go back`。
3. `commands.go`：`handleLSP(sessionID)` 照 `handleMCP`；`run.go` 的 slash 分派加 `case "lsp": _ = cmds.handleLSP(state.sessionID)`。`c.session` 的类型是 `pkg/tui/notify.go:1252 type Session interface`，在其 `HandleMCPSlash`（:1322）之后加 `HandleLSPSlash(sessionID, channel string) (string, bool)`；`pkg/tui` 测试里实现 `Session` 的替身都要补上这个方法（`grep -rn "HandleMCPSlash" pkg/tui/*_test.go` 找出它们）。

**验证**：`CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -count=1` → `ok`。

### 步骤 5：gateway

1. `api_extra.go` 路由表在 `mcp := …` 组之后加：

   ```go
   lsp := api.Group("/v1/lsp")
   lsp.Get("/", s.handleLSPSnapshot)
   lsp.Post("/servers/enable", s.handleLSPServerEnable)
   lsp.Post("/servers/disable", s.handleLSPServerDisable)
   lsp.Post("/servers/restart", s.handleLSPServerRestart)
   lsp.Post("/servers/install", s.handleLSPServerInstall)
   lsp.Post("/recommendations/reset", s.handleLSPRecommendationsReset)
   ```

   - 共用 `s.lspControlFor(r *http.Request, sessionID string) (tool.CodeIntelControl, bool)`：Runner 解析同 `handleMCPServerToggle`；Runner 或其 `CodeIntelControl` 为 nil → 503 `language servers are not available`。
   - `GET /api/v1/lsp?session_id=…` → 200，JSON 为 `event.LSPSnapshot`。
   - `POST …/servers/*` 请求体 `{"id": "gopls", "session_id": "…"}`；`id` 为空 → 400 `id required`；`enable`/`disable` 调 `SetEnabled`；`restart` 调 `Restart`；`install` 在后台 goroutine 里调 `Install(context.WithoutCancel(r.Context()), id, nil)` 并立即返回 202 `{"ok": true, "started": true}`（进度与结果从快照读取）；其他成功返回 200 `{"ok": true}`；控制面返回错误 → 409 + 错误文本。
   - `POST /recommendations/reset` 体 `{"session_id": "…"}` → `ResetRecommendations`。
2. `slash_handlers.go`：`HandleLSPSlash` 照 `HandleMCPSlash`（:142）；状态报告构造（:92）加 LSP 快照（同终端）。
3. `server.go:2149` 的 `turn.Context` 加 `LSP: s`。

**验证**：`CGO_ENABLED=1 go test -tags fts5 ./pkg/gateway -run 'LSP|Status' -count=1` → `ok`。

### 步骤 6：Web 页面

1. `api.ts`：类型 `LspServerStatus`、`LspSnapshot`（字段与 `event.LSPServerStatus`/`LSPSnapshot` 的 json tag 一致，经 `toCamelCase` 转换）；函数 `lspSnapshot(sessionId?: string)`、`lspSetEnabled(id, enabled, sessionId?)`、`lspRestart(id, sessionId?)`、`lspInstall(id, sessionId?)`、`lspResetRecommendations(sessionId?)`，路径与步骤 5 一致（照 `mcpSetServerDisabled` 与 `forebrainApi.mcpServers` 的写法）。
2. `LspView.vue`（照 `McpView.vue` 的布局与样式类）：
   - 页头：标题、说明、刷新按钮；项目行（根目录与信任状态、`feature_enabled == false` 的提示）；「服务器在本机沙箱外运行，只在受信任项目中启动」的说明。
   - 列表：每个服务器一张卡片：id、语言、scope 徽标、状态徽标（颜色与 `McpView` 的 `statusClass` 同义）、二进制与版本、问题计数、`last_error`、日志路径（可复制）、`project_writes`。
   - 按钮：启用 / 停用、重启（条件同终端详情页）、安装（**先弹确认框**显示完整 `install_command` 与「在本机沙箱外运行」提示，确认后才调用 `lspInstall`）。
   - 有任何服务器 `installing` 时每 2 秒刷新一次，全部结束后停止；安装输出显示在该卡片下方（`install_log`、`install_error`）。
   - 推荐关闭时显示原因与「重新开启推荐」按钮。
3. 路由：`/lsp`，`name: 'lsp'`，`meta.title` 为 `routes.lspTitle`；`App.vue` 的 workspace 组在 MCP 之后加 `{ to: '/lsp', label: L.lsp, icon: Braces }`（`lucide-vue-next` 的 `Braces` 图标，与现有图标同一个包）；`navLabels` 加 `lsp: t('nav.lsp')`。
4. `locales/index.ts`：中英文各加 `nav.lsp`（`语言服务器` / `Language servers`）、`routes.lspTitle`（`Forebrain Harness · 语言服务器` / `Forebrain Harness · Language servers`）与页面用到的全部 `lsp.*` 键；两套键集合必须一致。

**验证**：`cd frontend && pnpm install --frozen-lockfile && pnpm test && pnpm build` → 退出码 0；然后 `git checkout -- pkg/gateway/dist`。

### 步骤 7：全量检查

**验证**：`gofmt`、`go vet ./...` 干净；`ls pkg/turn/*.go pkg/tui/*.go pkg/gateway/*.go | grep -v _test.go` 各包仍为 20 个；`scripts/package-graph.sh` 后提交 `graph.json`（只有 `loc` 变化）；`CGO_ENABLED=1 go test -tags fts5 ./... -count=1 -timeout 20m` → 全部 `ok`；`git status --short pkg/gateway/dist` 无输出。

## 测试计划

- `pkg/turn/mcp_format_test.go`：`TestLSPServerStateLabel`（八种状态 + installing + 问题计数的单复数）；`TestLSPStatusLine`（无启用 → `""`；`2 running, 1 indexing, 1 failed`）；`TestRenderLSPInventoryMarkdown`（golden：有项目受信任、未受信任、无项目、功能关闭、推荐关闭五种快照）。
- `pkg/turn/slash_test.go`：`/lsp` 在两个界面的命令列表中；类别 `tools`；动作 `open-panel`。
- `pkg/turn/executor_test.go`：`ctx.LSP == nil` → `lsp: unavailable`；有处理器时返回其文本。
- `pkg/turn/model_test.go`：`StatusFacts` 在 `LSP` 非空时有 `Language servers` 行、为空时没有，且位于 `MCP servers` 之后。
- `pkg/tui/render_test.go`：`buildUIPanel` 对 `lsp` 列表页与详情页的布局（照现有 MCP 面板测试，在固定宽度下断言关键行）。
- `pkg/tui/run_test.go`：面板动作——列表 Enter 进入详情；详情里 Disable 调用 `PanelSetLSPEnabled(id, false)` 且 notice 显示返回文本；Esc 从详情回列表并选中原行；`LSPStatusTickMsg` 触发重新取快照。
- `pkg/tui/chat_session_test.go`：各 `PanelLSP*` 在控制面为 nil 时返回 `errPanelUnavailable`；返回文本逐字。
- `pkg/gateway/api_extra_test.go`：`TestLSPSnapshotEndpoint`、`TestLSPServerActions`（enable/disable/restart 200；install 202 且控制面替身的 `Install` 被调用；缺 id 400；无控制面 503；控制面错误 409）、`TestLSPRecommendationsResetEndpoint`。
- `pkg/gateway/slash_handlers_test.go`：`/lsp` 文字回复；`/status` 含 `Language servers` 行。
- `frontend/src/views/LspView.test.ts`：渲染两种状态的服务器；启用按钮调用 `lspSetEnabled`；安装按钮在确认后才调用 `lspInstall`、取消确认则不调用；`installing: true` 时开始轮询、变为 false 后停止。

## 完成判据

- [ ] 上述测试全部通过；`pnpm test`、`pnpm build` 通过且 `pkg/gateway/dist` 无改动
- [ ] 终端输入 `/lsp` 打开面板；非视口模式下输出文字版
- [ ] Web 导航出现「Language servers」，页面可用
- [ ] `/status` 在有已启用服务器时出现 `Language servers` 行
- [ ] 满额包文件数不变；`graph.json` 已提交；全量测试通过
- [ ] README 中任务 14 状态为 `DONE`

## STOP 条件

- 需要在 `turn`、`tui`、`gateway` 新建生产文件或新增导入。
- 需要改变 `lsp` 工具表、系统提示或任何模型可见文本。
- 安装需要在用户看到完整命令之前开始。

## 维护说明

- 快照字段新增时，同时改 `RenderLSPInventoryMarkdown`、终端详情页、`LspView.vue` 三处，并补 golden。
- 面板动作都经 `CodeIntelControl`；不要让界面直接读写 `state/lsp/*.json`。
- 评审重点：Web 安装的确认框；面板在服务器状态频繁变化时不闪烁（订阅已有 100ms 去抖）。
