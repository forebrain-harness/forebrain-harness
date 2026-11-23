# Plan 111：TUI 的会话只跑在项目 runner 上——基础 runner 不再启动 MCP，状态行、`/mcp`、`/lsp`、Esc 跳过都看会话所属项目的 runner

> **执行者须知**：按步骤顺序执行，每步跑验证命令并确认预期结果后再进下一步。触发
> "STOP conditions" 时停下报告，不要即兴发挥。**不要更新 `plans/README.md`**（评审方维护索引）。
> **禁止提交代码**（owner 手动提交）：不 commit、不 push、不开 PR、不建分支、不用 worktree，
> 改动直接留在 dev 工作区。工作区里 `pkg/tui/run.go`、`pkg/tui/chat_session.go`、`pkg/tui/overlays.go`、
> `pkg/tui/render.go`、`pkg/run/*`、`pkg/gateway/*` 等**已有 owner 未提交的改动**，与本计划交织在同一批文件里：
> 只做本计划描述的修改，**不要回滚、重排或格式化你没改的代码**。
>
> **Drift check（先跑）**：本计划要改的 TUI 文件本身就有未提交改动，不能用 `git diff` 判漂移，改用锚点：
>
> ```bash
> grep -c 'func (s \*ChatSession) WatchMCPStartup(sessionID string)' pkg/tui/notify.go          # 1
> grep -c 'func (s \*ChatSession) SkipOptionalMCPStartup() bool' pkg/tui/notify.go               # 1
> grep -c 'func (s \*ChatSession) mcpInventorySource() turn.MCPInventorySource' pkg/tui/chat_slash.go  # 1
> grep -c 'func (s \*ChatSession) lspControl() tool.CodeIntelControl' pkg/tui/chat_session.go     # 1
> grep -c 'watchMCPStartup(initialSessionID)' pkg/tui/run.go                                      # 1
> grep -c 'watcher.WatchMCPStartup(next)' pkg/tui/run.go                                          # 1
> grep -c 'ExactLaunchRoot: true,' pkg/tui/notify.go                                              # 1
> grep -c 'func (p \*RunnerPool) BindSession(ctx context.Context, sessionID, projectID string) error' pkg/process/runner_pool.go  # 1
> grep -c 'mcpRes := ResolveSessionMCP(root, activeAgent.WorkspaceRoot, cfgRoot.Agents, activeAgent.ID, launchProject)' pkg/process/open.go  # 1
> ```
>
> 每行都必须是 `1`。不是就 STOP 1。

## Status

- **Priority**: P1（真实缺陷：同一组 MCP 服务在 TUI 里起两份；`112` 依赖它）
- **Effort**: L
- **Risk**: MED（改变 TUI 里「哪个 runner 承载 MCP」；回退方法是把 Step 1 的开关去掉）
- **Depends on**: none
- **Category**: bug（owner 2026-10-23：层级是 **会话 → 项目 → 主代理 → 全局**，所有会话都必须建立在项目之上）
- **Planned at**: commit `f18ff14`（dev 工作区含 owner 未提交改动），2026-10-23

## Why this matters

TUI 启动时，进程的**基础 runner**（`process.Environment.Runner`）在 `process.Open` 里 `Load()`，启动一整组 MCP 服务；
状态行、`/mcp`、`/lsp`、Esc 跳过、MCP 认证都读这个 runner。可是 TUI 启动时 `AdoptLaunchProject` 把会话默认挂到「启动目录」这个项目上，
于是**第一轮对话**经 `RunnerForTurn` 走项目 runner 池，**另建一个 runner 再 `Load()` 一遍**，同一组 MCP 服务又起一份——对话真正用的是这一份，
而界面看的是另一份。

写计划时用真二进制实测过（`.claude/skills/run-forebrain/mcp_acc.sh start --with-mcp reply`，两个 `/bin/sleep 60` 假 MCP 服务）：

- 首轮前：`pgrep -f '^/bin/sleep 60' | wc -l` → **2**
- 发一轮之后 5 秒：→ **4**（两组，父进程都是同一个 forebrain；`info.log` 里同一个 `mcp_generation` 出现 3 次 `agent load`）
- 此时状态行还显示基础 runner 那组的 `mcp 0/2 · connecting … · 13s · esc to skip`，而这一轮在等的是项目 runner 那组（5 秒前才起）——Esc 跳过取消的是**另一组**。

owner 的裁定：会话 → 项目 → 主代理 → 全局；会话的运行时只来自所属项目的 runner，基础 runner 是主代理级的，**不承载任何 MCP**。
本计划在 TUI 里落实它；网关同样的问题（基础 runner + 每个项目 runner 各起一份全局服务）在 `plans/114` 里随网页改动一起修，并在那里删掉本计划引入的过渡开关。

## Current state

**基础 runner 的 MCP**（`pkg/process/open.go:339-401` 节选）：

```go
	mcpRes := ResolveSessionMCP(root, activeAgent.WorkspaceRoot, cfgRoot.Agents, activeAgent.ID, launchProject)
	frozenMCP := mcpRes.Servers
	...
	env.Deps = run.Deps{
		...
		MCPServers:  frozenMCP,
		MCPProject:  mcpRes.ProjectRoot,
		MCPDisabled: mcpRes.Disabled,
		...
		MCPDiagnostics: func(frozen []appcfg.MCPServerConfig) mcp.ProjectMCPScopeSummary { ... },
		MCPResolve: func(agentName, agentWorkspace string, launch safety.ProjectContext) (run.MCPServersResolution, error) {
			agents := env.Deps.AppCfg.Agents
			res := ResolveSessionMCP(root, agentWorkspace, agents, agentName, launch)
			return run.MCPServersResolution{Servers: res.Servers, Disabled: res.Disabled}, nil
		},
```

`run.Runner.Load` 在后台启动 `MCPServers`（`pkg/run/runner.go` 的 `startOrReplayMCPSegmentLocked`）；`MCPResolve` 是「会话开始时重新解析」的钩子，
nil 时 runner 保持构建时的列表。**列表为空时 Load 不起任何进程**（`newMCPLoadLocked` 没有任务就直接给空段）。

**TUI 怎么开环境**（`pkg/tui/notify.go:1523-1532`，`openProcessChatSession`）：

```go
	env, err := process.Open(ctx, process.OpenOptions{
		Config: &cfg,
		...
		LaunchDir:       cwd,
		ExactLaunchRoot: true,
		SessionSource:   memory.SessionSourceTUI,
	})
```

网关在 `pkg/gateway/serve_run.go:35` 也调 `process.Open`——**本计划不改网关**，所以新行为必须是一个只有 TUI 打开的选项。

**项目 runner 池**（`pkg/process/runner_pool.go`）：

- `BindSession(ctx, sessionID, projectID)`（`:1159-1185`）：记下会话属于哪个项目并**预先建好**该项目的 runner（`entryForSession`，会 `Load()`、起 MCP）。网关在会话绑定项目时用它。
- `BoundRunnerForSession(ctx, sessionID)`（`:600-621`）：**只**经数据库 `p.projects.ProjectForSession` 找项目——TUI 的新会话在首轮之前**没有 `fb_sessions` 行**
  （没用过的会话不落库，`pkg/tui/run.go` 的 `sessionTouched` 逻辑依赖这一点），所以它会返回 `nil, false`，即使 `BindSession` 已把会话记进内存表 `p.sessions`。
- `RunnerForSessionOnEnv` 的注释（`:137-140`）还写着「The TUI never triggers pool creation: its sessions have no project row」——已过时（`AdoptLaunchProject` 之后 TUI 会话都有项目）。
- `RebindAgent(agentID)`（`:1189`）：关掉所有池 runner、换主代理。网关切主代理时在 `OnApplied` 里调它（`pkg/gateway/agents_api.go:250-262`）；**TUI 的 `/agent` 切换没调**
  （`pkg/tui/chat_slash.go:848-858` 的 `agentSwitchDeps` 没有 `OnApplied`）。

**TUI 里读 MCP/LSP 运行状态的地方全都用基础 runner `s.runner()`**（`pkg/tui/chat_session.go:703-708`，返回 `s.Env.Runner`）：

| 位置 | 函数 | 读什么 |
|---|---|---|
| `pkg/tui/notify.go:1267` | `WatchMCPStartup(sessionID)` | 订阅 `MCPStartup()`，驱动状态行与启动失败归属 |
| `pkg/tui/notify.go:1432` | `SkipOptionalMCPStartup()` | Esc 跳过可选服务 |
| `pkg/tui/chat_slash.go:146` | `mcpInventorySource()` | `/mcp` 与 `/status` 的 MCP 清单 |
| `pkg/tui/chat_slash.go:97` | `statusSource(cfg, sessionID, side)` | 其中 `ConfigFiles: ... r.MCPProjectStatus()` 与 `r.CodeIntelControl`（LSP 快照）两处 |
| `pkg/tui/chat_slash.go:1158` | `startMCPLocalOAuth` | 找服务配置、判断是否已注册 |
| `pkg/tui/chat_session.go:2939` | `PanelMCPInventory` | 只做 nil 检查 |
| `pkg/tui/chat_session.go:2965` | `PanelSetMCPDisabled` | 列表 |
| `pkg/tui/chat_session.go:2975` | `PanelListMCPResources` | `MCPRegistry()` |
| `pkg/tui/chat_session.go:3001` | `SubscribeMCPStatusTick` | `MCPStartup()` |
| `pkg/tui/chat_session.go:3022` | `lspControl()` | `/lsp` 面板的全部操作 |
| `pkg/tui/chat_session.go:601` | `ReconcileSessionMCP` | 增量对齐 MCP |

其余几十处 `s.runner()`（模型、权限、技能、子代理名册……）是主代理级的读取，**不在本计划范围**。

**启动与切会话**（`pkg/tui/run.go`）：

- `watchMCPStartup(initialSessionID)`（约 `:620-625`）在视口模式和欢迎界面**之前**就订阅了基础 runner；
- 欢迎界面在 `renderer.Banner(startupInfo)` / `renderer.StartupInfo` / `renderer.StartupHint()`（约 `:666-672`），之后是 `printSessionResumeContext`、`opts.Session.StartApprovalRecovery(state.sessionID)`；
- `switchStreamSession`（约 `:1757-1806`）在 `state.session.SetViewingSession(next)` 之后调 `watcher.WatchMCPStartup(next)`。
- `WatchMCPStartup` 里有「同一会话已在看就直接返回」的短路（`s.mcpWatchSession == sid && s.mcpWatchCancel != nil`）——换了 runner 也会被它挡住。

**架构护栏**：`pkg/architecture/cache_test.go` 的扇出上限只许变小（`tui` 19、`process` 19）。本计划**不需要也不允许**给 `pkg/tui` / `pkg/process` 加新的 `pkg/` import；
`run.Runner` 的字段数也有护栏（`TestRunnerOnlyShrinks`），**不要给 `run.Runner` 加字段**。

## Commands you will need

| Purpose | Command | Expected |
|---|---|---|
| Build | `go build ./...` | exit 0 |
| 单测 | `go test -tags fts5 ./pkg/process/ ./pkg/tui/ ./cmd/forebrain/` | `ok` ×3（写计划时基线全绿） |
| 护栏 | `go test ./pkg/architecture/` | `ok` |
| 格式 | `gofmt -l pkg/process pkg/tui` | 无输出 |
| 包图 | `scripts/package-graph.sh && git diff -- pkg/architecture/testdata/graph.json \| grep '^[+-]' \| grep -v '"loc"' \| grep -v '^[+-][+-]'` | 无输出 |
| 真机验收 | 见 Step 7 | 见 Step 7 |

## Suggested executor toolkit

- `run-forebrain` 技能（`.claude/skills/run-forebrain/SKILL.md`）：驱动真 TUI。本计划用它目录里的 `mcp_acc.sh`（隔离 home、假模型、tmux），
  **把 `FOREBRAIN_MCP_ACC_WORK` 指到你自己的临时目录**，不要写进仓库。

## Scope

**In scope**：`pkg/process/open.go`、`pkg/process/runner_pool.go`、`pkg/process/open_test.go`、`pkg/process/runner_pool_test.go`、
`pkg/tui/notify.go`、`pkg/tui/chat_session.go`、`pkg/tui/chat_slash.go`、`pkg/tui/run.go`、`pkg/tui/chat_session_test.go`（或新建 `pkg/tui/conversation_runtime_test.go`）、
`pkg/architecture/testdata/graph.json`（只由脚本重新生成）。

**Out of scope**：
- 网关（`pkg/gateway/*`、`pkg/gateway/serve_run.go` 的 `process.Open` 调用）——`114`。
- 项目确认面板——`112`；不要碰 `askProjectMCPConsentBeforeSwitch`、`cmd/forebrain/mcp_consent.go`。
- 模型选择/激活（`ActivateSessionModel`、`sessionModelState`、`SelectModel`）——它们与基础 runner 的关系不变。
- `run.Runner` / `run.Deps` 的结构。

## Steps

### Step 1：`process.Open` 加过渡开关——基础 runner 不承载 MCP

`pkg/process/open.go`：

1. `OpenOptions` 加字段（放在 `ExactLaunchRoot` 之后）：

   ```go
	// AgentRunnerWithoutMCP keeps the environment's own runner from hosting
	// MCP servers. That runner is the primary agent's: it serves no
	// conversation — every conversation runs on its project's runner (see
	// RunnerPool) — so servers started here would be a second copy of the
	// ones the conversation actually uses. The interactive surface sets it;
	// the gateway follows in plans/114, which then removes this option.
	AgentRunnerWithoutMCP bool
   ```
2. 把 `mcpRes := ResolveSessionMCP(...)` 改为只在 `!opts.AgentRunnerWithoutMCP` 时解析（否则 `mcpRes` 是零值，`frozenMCP` 为 nil）。
3. `env.Deps = run.Deps{...}` 赋值之后、`runner := &run.Runner{...}` 之前：

   ```go
	if opts.AgentRunnerWithoutMCP {
		// No re-resolution either: a conversation that begins re-resolves on
		// its project's runner, never here.
		env.Deps.MCPResolve = nil
		env.Deps.MCPDiagnostics = nil
	}
   ```

**Verify**：`go build ./... && go test -tags fts5 ./pkg/process/` → `ok`（开关默认关，行为不变）。

### Step 2：池能认出「已绑定但还没落库」的会话，并提供一个绑定入口

`pkg/process/runner_pool.go`：

1. `(*RunnerPool).BoundRunnerForSession`：在 `base := p.env.Runner` / 空参数判断之后、查数据库之前，先查内存绑定：

   ```go
	// A session bound ahead of its first turn (BindSession) has no row yet;
	// the binding itself is the answer.
	p.mu.Lock()
	if key, ok := p.sessions[sessionID]; ok {
		if entry := p.entries[key]; entry != nil && entry.runner != nil {
			p.mu.Unlock()
			return entry.runner, true
		}
	}
	p.mu.Unlock()
   ```
2. 新增（放在 `(*Environment).BoundRunnerForSession` 之后）：

   ```go
// BindConversation binds sessionID to projectID ahead of its first turn and
// returns the project's runner, building it — and so starting its MCP servers
// — when it does not exist yet. It is how a surface that opens a conversation
// before anything is said gets that conversation's runtime up front.
func (env *Environment) BindConversation(ctx context.Context, sessionID, projectID string) (*run.Runner, error)
   ```

   实现：`env == nil` 或 `env.SQL == nil` → `return env.Runner, nil`（无注册表的进程）；`env.EnsureRunnerPool()`；
   `pool.BindSession(ctx, sessionID, projectID)` 出错原样返回；然后 `r, ok := env.BoundRunnerForSession(ctx, sessionID)`，
   `!ok || r == nil` → `return nil, fmt.Errorf("runner pool: session %s has no runner after binding", sessionID)`。
3. 改正 `RunnerForSessionOnEnv`（`(*Environment).RunnerForSession`）上方的过时注释：TUI 的会话属于启动目录这个项目，转轮次在项目 runner 上；
   基础 runner 只回答主代理级的读取。

**Verify**：`go test -tags fts5 ./pkg/process/ -run 'RunnerPool|Bound'` → `ok`。

### Step 3：TUI 打开开关，加「会话所属的 runner」读取口

1. `pkg/tui/notify.go` 的 `openProcessChatSession`：`process.OpenOptions{...}` 里加 `AgentRunnerWithoutMCP: true,`（带一行注释：会话的 MCP 在项目 runner 上）。
2. `pkg/tui/chat_session.go`，`runner()` 旁边新增：

   ```go
// conversationRunner is the runner sessionID's conversation runs on: its
// project's runner once bound (BindConversationRuntime), else the
// environment's own — a harness with no project registry, or a test fixture.
// Everything that shows or acts on a conversation's MCP servers and language
// servers reads this, never runner(): the environment's own runner hosts none.
func (s *ChatSession) conversationRunner(sessionID string) *run.Runner

// sessionRunner is conversationRunner for the conversation on screen.
func (s *ChatSession) sessionRunner() *run.Runner

// BindConversationRuntime binds sessionID to this surface's project and makes
// sure its runner exists — which starts its MCP servers — then reconciles that
// runner's MCP half the way a conversation that is beginning does, so a
// configuration change lands here rather than one conversation later.
func (s *ChatSession) BindConversationRuntime(ctx context.Context, sessionID string) error
   ```

   - `conversationRunner`：`s == nil || s.Env == nil` → `s.runner()`；sid 为空 → `s.runner()`；
     `if r, ok := s.Env.BoundRunnerForSession(context.Background(), sid); ok && r != nil { return r }`；否则 `s.runner()`。
   - `sessionRunner`：`return s.conversationRunner(s.currentSessionID())`。
   - `BindConversationRuntime`：`s.Env == nil || s.Env.SQL == nil || strings.TrimSpace(s.ProjectID) == ""` → `return nil`；
     `r, err := s.Env.BindConversation(ctx, sid, s.ProjectID)`；出错返回；`return r.ReconcileSessionMCP()`。

**Verify**：`go build ./...` → exit 0。

### Step 4：把表格里每一处 MCP/LSP 读取换成会话的 runner

逐个改，**只换 runner 来源，不改别的逻辑**：

1. `WatchMCPStartup(sessionID)`：`runner := s.runner()` → `runner := s.conversationRunner(sid)`；并把开头的短路改成「同一会话**且同一 runner**才短路」：
   在 `ChatSession` 结构体里 `mcpWatchSession` 旁加字段 `mcpWatchRunner *run.Runner`，短路条件加 `&& s.mcpWatchRunner == runner`
   （runner 要在短路判断**之前**取出来），订阅成功后记下 `s.mcpWatchRunner = runner`。改动处加注释：一个会话绑定后换了 runner（首次绑定、切主代理），watch 必须跟过去。
2. `SkipOptionalMCPStartup()`：`s.runner()` → `s.sessionRunner()`。
3. `mcpInventorySource()`：`r := s.runner()` → `r := s.sessionRunner()`。
4. `statusSource(cfg, sessionID, side)`：在 `r := s.runner()` 之后加 `cr := s.conversationRunner(sid)`（`sid` 在下一行已定义，按需调整顺序），
   **只把** `turn.StatusConfigFiles(cfg, r.MCPProjectStatus().ProjectRoot)` 改成 `cr.MCPProjectStatus()`，把 `if ctl := r.CodeIntelControl` 改成 `cr.CodeIntelControl`；其余字段仍用 `r`。
5. `startMCPLocalOAuth`：`r := s.runner()` → `r := s.sessionRunner()`。
6. `PanelMCPInventory`：nil 检查改用 `s.sessionRunner()`。
7. `PanelSetMCPDisabled`、`PanelListMCPResources`、`SubscribeMCPStatusTick`：`s.runner()` → `s.sessionRunner()`。
8. `lspControl()`：`r := s.runner()` → `r := s.sessionRunner()`。
9. `ReconcileSessionMCP()`（`chat_session.go:601`）：`s.runner()` → `s.sessionRunner()`，注释同步。

**Verify**：`go build ./... && go test -tags fts5 ./pkg/tui/` → `ok`（没有绑定时 `conversationRunner` 回落到基础 runner，既有测试不该变红）。

### Step 5：启动、切会话、切主代理时绑定会话的 runner

`pkg/tui/run.go`：

1. 新增可选接口（放在 `mcpStartupWatcher` 旁边）：

   ```go
// conversationRuntimeBinder is the session half that brings a conversation's
// runtime up: its project's runner, built when it does not exist yet.
type conversationRuntimeBinder interface {
	BindConversationRuntime(ctx context.Context, sessionID string) error
}
   ```

   和一个函数：

   ```go
// startConversationRuntime binds the conversation to its project's runner and
// points the MCP status line at that runner. A bind that fails is reported and
// the surface carries on: the conversation's first turn reports the same
// failure through RunnerForTurn.
func startConversationRuntime(ctx context.Context, session Session, renderer *Renderer, sessionID string)
   ```

   实现：`if b, ok := session.(conversationRuntimeBinder); ok { if err := b.BindConversationRuntime(ctx, sessionID); err != nil && renderer != nil { renderer.PrintError(err) } }`，
   然后 `if w, ok := session.(mcpStartupWatcher); ok { w.WatchMCPStartup(sessionID) }`。
2. `Run`：删掉原来在视口模式之前的 `watchMCPStartup(initialSessionID)` 调用（以及只为它存在的 `watchMCPStartup` 闭包，若再无其他用处）；
   在欢迎界面与 `printSessionResumeContext` **之后**、`opts.Session.StartApprovalRecovery(state.sessionID)` **之前**调
   `startConversationRuntime(ctx, opts.Session, renderer, state.sessionID)`。在调用处留注释：确认面板（`plans/112`）会插在这一行之前。
   **保持**原注释表达的约束：订阅必须在 notify sink 建好之后（新位置天然满足）。
3. `switchStreamSession`：把 `if watcher, ok := state.session.(mcpStartupWatcher); ok { watcher.WatchMCPStartup(next) }` 换成
   `startConversationRuntime(ctx, state.session, renderer, next)`，注释改为：会话的运行时跟着会话走——绑定到它项目的 runner，状态行看那个 runner。

`pkg/tui/chat_slash.go`（切主代理）：

4. `agentSwitchDeps()` 加 `OnApplied`，照网关 `pkg/gateway/agents_api.go:250-262` 的写法：池存在就 `pool.RebindAgent(active.ID)`（注释：池里的 runner 属于前一个主代理的存储与确认记录）。
5. `SwitchPrimaryAgent`：`AdoptLaunchProject` 成功之后，调 `s.BindConversationRuntime(ctx, s.currentSessionID())`（出错就返回该错误）和 `s.WatchMCPStartup(s.currentSessionID())`。

**Verify**：`go build ./... && go test -tags fts5 ./pkg/tui/ ./cmd/forebrain/` → `ok` ×2。

### Step 6：单测

见下方 Test plan，全部写完并通过。

**Verify**：`go test -tags fts5 ./pkg/process/ ./pkg/tui/` → `ok` ×2；`go test ./pkg/architecture/` → `ok`；`gofmt -l pkg/process pkg/tui` → 无输出。

### Step 7：真机验收（必须做）

```bash
export FOREBRAIN_MCP_ACC_WORK=<你的临时目录>/mcpdup FOREBRAIN_MCP_ACC_SESSION=fb-111 FOREBRAIN_MCP_ACC_PORT=8794
A=".claude/skills/run-forebrain/mcp_acc.sh"
bash $A build
REQ_TIMEOUT=20 OPT_TIMEOUT=120 bash $A start --with-mcp reply    # 记下输出的 first_frame_after_trust_secs
sleep 3; pgrep -f '^/bin/sleep 60' | wc -l                        # 期望 2（项目 runner 在启动时就绑好、起好）
bash $A screen | grep 'mcp 0/2'                                   # 状态行在
bash $A submit 'hello'; sleep 5; pgrep -f '^/bin/sleep 60' | wc -l  # 期望仍是 2（修复前是 4）
bash $A key Escape; sleep 1; bash $A screen | tail -8             # Esc 跳过作用在本轮等待的那组：optional-slow 显示为跳过，
                                                                  # 本轮继续等 required-docs（修复前跳过的是另一组，本轮照等）
sleep 25                                                          # required-docs 在 20s 超时，本轮以错误结束、回到输入框
bash $A submit '/new'; sleep 3; pgrep -f '^/bin/sleep 60' | wc -l  # 期望 ≤ 2：新会话同项目、同一个 runner；
                                                                  # 进入会话时的对齐会重启超时的那个服务（与网关一致），但绝不会出现第二组
bash $A stop; sleep 1; pgrep -f '^/bin/sleep 60' | wc -l           # 期望 0：退出后没有遗留子进程
```

**不变式**：整个过程中任何时刻 `pgrep -f '^/bin/sleep 60' | wc -l` 都不超过 2（配置了 2 个服务）。
记录 `first_frame_after_trust_secs`，与写计划时的 1.03s 比较；若多出 1s 以上，STOP 6。

## Test plan

模式参照：`pkg/process/runner_pool_test.go` 的 `newPoolTestEnv` / `ensurePoolSessions` / `TestRunnerPoolProjectMCPServesProjectServers`；
`pkg/process/open_test.go:160-180`（临时 home + `Open`）；TUI 侧 `pkg/tui/chat_session_test.go` 的 `newModelSelectionTestSession` 与
`TestFooterNamesTheConfiguredDefaultBeforeAnyProjectRunner`（建项目、`projects.BindSession`）。MCP 假服务用 `command: /usr/bin/true` 的 stdio 条目即可（立即退出、不挂）。

1. `pkg/process/open_test.go`：`TestOpenAgentRunnerWithoutMCPHostsNoServer`——配置一个全局 stdio MCP 条目，`Open(..., AgentRunnerWithoutMCP: true)` →
   `env.Runner.MCPServers` 为空、`env.Deps.MCPResolve == nil`、`env.Runner.MCPStartup().Snapshot().Servers` 为空。
2. `pkg/process/runner_pool_test.go`：`TestRunnerPoolBoundRunnerForSessionSeesUnpersistedBinding`——对一个**没有** `fb_sessions` 行的会话 `BindSession` →
   `BoundRunnerForSession` 返回该项目条目的 runner、`ok == true`，且与随后 `ensurePoolSessions` + `projects.BindSession` 后 `RunnerForTurn` 返回的是**同一个指针**。
3. `pkg/process/runner_pool_test.go`：`TestEnvironmentBindConversationBuildsTheProjectRunner`——`BindConversation` 返回的 runner 的 `MCPServers` 含全局条目（项目 runner 承载 MCP），且不等于 `env.Runner`。
4. `pkg/tui`：`TestConversationRunnerFollowsTheBoundProjectRunner`——`newModelSelectionTestSession`，建项目、`s.ProjectID = project.ID`，
   `BindConversationRuntime(ctx, "sess-x")` → `s.conversationRunner("sess-x") != s.runner()`；`SetViewingSession("sess-x")` 后 `s.sessionRunner()` 是同一个。
5. `pkg/tui`：`TestWatchMCPStartupFollowsARunnerChange`——同一 sid 先在未绑定时 watch（基础 runner），绑定后再 watch → 订阅换到项目 runner（可通过 `s.mcpWatchRunner` 断言）。
6. `pkg/tui`：`TestSwitchStreamSessionBindsBeforeWatching`——一个同时实现 `conversationRuntimeBinder` 与 `mcpStartupWatcher` 的假会话，记录调用顺序 → `bind:sess-b` 在 `watch:sess-b` 之前，且都只调一次。
7. `pkg/tui`：`TestPanelMCPInventoryReadsTheConversationRunner`——绑定后 `PanelMCPInventory()` 的服务清单来自项目 runner（与 `s.runner().MCPServers` 为空对照）。

**Verify**：`go test -tags fts5 ./pkg/process/ ./pkg/tui/` → `ok`，上述 7 个用例存在并通过。

## Done criteria

- [ ] `go build ./...` exit 0
- [ ] `go test -tags fts5 ./pkg/process/ ./pkg/tui/ ./cmd/forebrain/` 全 `ok`，含 7 个新用例
- [ ] `go test ./pkg/architecture/` `ok`；`gofmt -l pkg/process pkg/tui` 无输出；包图只有 `loc` 变化
- [ ] `grep -n 's\.runner()' pkg/tui/notify.go pkg/tui/chat_slash.go pkg/tui/chat_session.go` 的结果里，不再出现于 Step 4 列出的 11 个函数体内（逐个核对）
- [ ] Step 7 的数字：启动后 2、首轮后 2、`/new` 后 ≤ 2、退出后 0，全程不超过 2；Esc 跳过作用在本轮等待的那组上
- [ ] `git status --short` 只多出 Scope 内的文件改动

## STOP conditions

1. Drift check 有任一行不是 `1`。
2. `BindSession` 对没有 `fb_sessions` 行的会话报外键或「session not found」类错误（说明池绑定要求先落库——那会和「没用过的会话不落库」冲突，需要 owner 定）。
3. 改完后 Step 7 首轮后的进程数仍是 4，或启动后是 0（项目 runner 没在启动时建起来）。
4. 某个既有 TUI 测试断言「基础 runner 承载 MCP」且无法通过只换读取口来满足——列出测试名停下，不要删测试。
5. 需要给 `pkg/tui` / `pkg/process` 加新的 `pkg/` import，或给 `run.Runner` 加字段，护栏变红。
6. `first_frame_after_trust_secs` 比 1.03s 多出 1s 以上（启动变慢明显，需要 owner 决定是否改成后台绑定）。

## Maintenance notes

- 规则：**会话的 MCP/LSP 状态一律经 `conversationRunner(sid)` / `sessionRunner()` 读**；新增任何会话级的 MCP/LSP 读取都不要再用 `s.runner()`。评审时专门看这一点。
- `plans/114` 会让网关也打开同样的行为，然后删掉 `OpenOptions.AgentRunnerWithoutMCP`（成为唯一行为）；届时本计划在 `openProcessChatSession` 里加的那一行要一起删。
- `BindConversationRuntime` 每次进入会话都调 `ReconcileSessionMCP`：没有配置变化时它只做一次解析就返回；项目 runner 被同项目的多个会话共用，这与网关「每次绑定都对齐」一致。
- `plans/112` 的确认面板插在 `startConversationRuntime` 之前：先记下决定，再建 runner，新允许的服务就在第一次 `Load` 里，不需要额外对齐。
