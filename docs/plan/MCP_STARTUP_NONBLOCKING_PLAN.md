# TUI 启动被 MCP 服务器拉起阻塞：根因与修复方案

> 日期：2026-09-20
> 状态：待实施（本文件只写方案，不含任何代码改动）
> 基线：`main` / `d4779630`
> 范围：`pkg/run`、`pkg/mcp`、`pkg/config`、`pkg/tui`、`pkg/process`、`pkg/gateway`、`frontend`
>
> 本期一并修复定位过程中发现的既有缺陷：MCP 启动失败对用户完全不可见（§3.1）、MCP 连接没有任何超时（§3.2）、stdio 子进程生命周期不归 forebrain 所有（§3.3）、`mcp.Registry` 的 connecting/error 状态位是死代码（§3.4）、SessionStart 钩子执行期无任何界面反馈（§3.5）。

---

## 1. 现场与证据

### 1.1 现象

启动 `forebrain`，终端标题变成 `npm exec caveman-shrink · node`，长时间黑屏，之后 TUI 才出现。

### 1.2 配置来源

`<project>/.forebrain/mcp_servers.yaml`：

```yaml
mcp_servers:
- name: caveman-shrink
  transport: stdio
  command: npx
  args:
  - -y
  - caveman-shrink
```

`npx -y` 即 `npm exec`，会走一次 npm registry 解析（网络），这就是终端标题的来源。

### 1.3 日志实测（`~/.forebrain/logs`）

```
10:28:37.909  INFO  cmd/forebrain/interactive.go:96   opening chat session for streaming terminal
10:28:37.995  INFO  pkg/llm/tokestimate.go:192     tokestimate: ready            ← 已进入 loadLocked
                                                   （此后 70 秒内没有任何一条日志）
10:29:47.917  ERROR pkg/run/runner.go:1583         mcp server start server=caveman-shrink
                                                   err="calling \"initialize\": EOF"
10:29:47.947  INFO  pkg/run/runner.go:1145         agent tools skills=71 mcp=0
```

**`runner.Load()` 里有 69.9 秒完全花在 `caveman-shrink` 这一个 MCP 服务器上，且最终以失败告终、注册到 0 个工具。TUI 在这 70 秒里一个字都没画。**

同一台机器上手工执行（npm 缓存已热）只要 2.1 秒，并且立刻报 `missing upstream command` 后退出 —— 该包不是 MCP server，永远不可能握手成功。也就是说：**每次启动都要为一个注定失败的服务器付出几十秒，而用户看不到任何解释。**

`error.log` 里这条失败从 2026-09-15 起重复了 10 次，用户从未在界面上看到过它。

---

## 2. 根因

### 2.1 调用链

```
cmd/forebrain/interactive.go:96   runStreamingTerminalWithInitialSessionID
  └─ interactiveOpenChatSession
       └─ tui.OpenChatSessionWithConfigForProject      pkg/tui/chat_session.go:218
            └─ openProcessChatSession                  pkg/tui/notify.go:907
                 └─ process.Open                       pkg/process/open.go
                      └─ runner.Load()                 pkg/process/open.go:300   ← 同步阻塞点
                           └─ loadLocked               pkg/run/runner.go:767
                                └─ loadMCPSegmentLocked pkg/run/runner.go:1131 / :1554
                                     └─ mcp.Start       pkg/mcp/bridge.go:90
                                          └─ exec + initialize 握手（串行、无超时）
  ↓ 以上全部返回之后
  └─ tui.Run(...)                                      ← 终端这时才开始画
```

### 2.2 根因一：MCP 段位于 TUI 首帧的关键路径上，且逐个串行

`Runner.loadLocked` 在构建 agent 的过程中同步调用 `loadMCPSegmentLocked`，后者 `for _, srv := range r.MCPServers` **逐个** `mcp.Start` + `ListToolMetas`。

这是 `Load()` 里唯一一段做**外部、无界 I/O**（拉起任意用户指定的子进程、连任意远端）的代码；其余部分（配置解析、LLM client 构造、技能目录发现）都是本地且毫秒级的。把它放在首帧之前，等于把 forebrain 的启动时间外包给了用户配置的第三方进程和 npm registry。

N 个服务器还要串行相加。

### 2.3 根因二：连接没有任何时间上界

`loadLocked` 传给 `loadMCPSegmentLocked` 的是 `baseCtx := context.Background()`（`pkg/run/runner.go:1130`），一路透传到 `mcp.Start` → `Client.Connect` → `initialize` 请求。**没有 deadline**。

一个只 `Start()` 成功、之后永不回应 `initialize` 的服务器（网络挂起的 `npx`、卡在 OAuth 的 HTTP 端点、写错命令的 stdio server），会让 forebrain 永久停在启动阶段，用户只能 Ctrl+C。

对照组：用户钩子在 `pkg/hook/runtime.go:238` 有默认 10 分钟、可 per-hook 覆盖的超时。**外部子进程都有超时，唯独 MCP 没有** —— 这是遗漏，不是设计。

---

## 3. 定位过程中发现的既有缺陷（本期一并修）

### 3.1 MCP 启动失败对用户完全不可见

`loadMCPSegmentLocked` 对 `mcp.Start` 失败只做 `slog.Error`（`runner.go:1583`），对 `tools/list` 失败同理。用户界面上没有任何提示。上面那条重复了 10 次、每次烧掉几十秒的失败，只存在于 `~/.forebrain/logs/error.log` 里。

修复要求：失败必须落进 TUI（和 Web）的会话记录，并且**原样展示底层报错原文**（`calling "initialize": EOF`），不套一层自己编的解释。

### 3.2 没有超时（同 §2.3）

单独列出，因为它即便在异步化之后也必须修：异步化只是把等待从"启动前"挪到"首轮前"，等待本身仍然需要时间上界。

### 3.3 stdio 子进程的生命周期不归 forebrain 所有

`pkg/mcp/bridge.go:113` 用的是 `exec.Command`，不是 `exec.CommandContext`。子进程的存活完全托付给 SDK 的 `ClientSession.Close()`。

SDK `Client.Connect`（`go-sdk@v1.5.0` `mcp/client.go:255`）在 `initialize` 报错时会 `cs.Close()`，但在**协议版本不受支持**这一条分支（`client.go:276-278`）直接 `return nil, err` 而**没有 Close** —— 那个 `npx` 进程就此成为孤儿，跟随 forebrain 进程活到天亮。

这不是给 SDK 打补丁的问题，而是所有权问题：forebrain 拉起的子进程，生命周期应当由 forebrain 自己持有，不能取决于依赖库某条分支写没写 Close。

### 3.4 `mcp.Registry` 的 connecting / error 状态是死代码

`pkg/mcp/registry.go:13-17` 定义了 `ConnStatusConnecting` / `ConnStatusError`，`ServerRecord` 有 `Error` 字段 —— 全仓库没有任何一处写入它们。`Register` / `RegisterMirror` 只会写 `ConnStatusConnected`。

于是 `/mcp`、`/status`、`turn.MCPConfiguredConnected` 只有"连上了"和"不在表里"两态，无法区分"正在连"和"连失败了，原因是 X"。本期正好需要这几态，把这批字段用起来，而不是另起一套并行状态。

### 3.5 SessionStart 钩子执行期间界面无反馈

`ChatSession.ensureSessionStartHooks`（`pkg/tui/chat_session.go:474`）在**首轮 dispatch** 时同步执行用户的 SessionStart 钩子（`pkg/tui/chat_surface.go:53`）。它不挡 TUI 启动（这点与 MCP 不同），但钩子默认超时 10 分钟，期间界面上什么都不显示，用户只看到回车之后没反应。

它与 MCP 启动是同一类事：**forebrain 替用户自动跑的后台工作**。本期用同一条实时状态行呈现（§6.7）。

---

## 4. 目标与硬约束

### 4.1 目标

| # | 目标 | 可测量的验收 |
|---|---|---|
| G1 | TUI 首帧不等任何 MCP 服务器 | 从 `opening chat session` 到首帧 < 1s（当前实测 70s） |
| G2 | 任何 MCP 服务器都不能让 forebrain 无限期停住 | 每台服务器有确定的时间上界 |
| G3 | 正在自动执行的后台工作在 TUI 里实时可见、可中断 | 组合器上方有实时状态行，显示在拉起谁、已用多久、Esc 可中断 |
| G4 | 多台 MCP 服务器并行拉起 | N 台的总就绪时间 ≈ max，而非 Σ |
| G5 | 提示词前缀缓存命中率**只增不减** | §7 的字节级 golden + 实测 |
| G6 | Web 与 TUI 同语义、同数据 | 同一份 Runner 状态，两个 surface 各自渲染 |

### 4.2 硬约束：工具表是提示词前缀的一部分

MCP 工具进入 agent 的工具表，工具表渲染在请求最前面（tools → system → messages），缓存断点压在 system 块末尾 —— tools + system 这一整段是缓存命中的主力。

因此绝不允许：**"先不带 MCP 工具开跑，等服务器连上了再把工具加进去"**。那会在会话中途改写工具表，一次性作废整段缓存前缀，把本次会话此前所有轮次的缓存全部打掉。

本方案的做法：

> **连接异步，工具表按会话冻结。** 连接过程移出启动关键路径；工具表在**第一次 LLM 请求发出之前**落定，此后整个会话不再变化。第一次请求看到的工具表，与今天同步加载时看到的**逐字节相同**。

省掉的是"用户盯着黑屏的时间"，不是"模型等待的时间"；而这段等待现在是**可见的、有上界的、可以一键跳过的**。

---

## 5. 方案总览

```
今天：
  [配置/技能/LLM 构造] → [MCP 串行连接 70s] → [TUI 首帧] → 用户输入 → [第一次请求]
                          ^^^^^^^^^^^^^^^^^^ 用户盯着黑屏

改后：
  [配置/技能/LLM 构造] → [TUI 首帧] ──────────────────→ 用户输入 → [落定] → [第一次请求]
            └─ 后台：[MCP 并行连接，每台有超时] ─────────────────────┘
                     │
                     └─→ 组合器上方实时状态行：
                         ⠋ 拉起 MCP 服务器 caveman-shrink（12s • esc 跳过）
                         ⠙ 拉起 MCP 服务器（1/3）：alpha、beta（4s • esc 跳过）
                     └─→ 失败落进会话记录：mcp caveman-shrink · calling "initialize": EOF
```

**Esc 的语义是"不等它了"**：中断仍在连接的服务器，工具表就以此刻已就绪的集合落定，会话继续。这把"跳过慢服务器"变成用户的显式动作，而不是一个看不见的计时器 —— 用户知道自己放弃了什么，而工具表依然只在会话开始时定一次。

六个部件：

1. **`pkg/mcp`**：registry 四态化（connecting / connected / failed / cancelled）+ 变更订阅（§6.6）
2. **`pkg/mcp`**：子进程生命周期归属（§6.4）+ 每服务器启动超时（§6.5）
3. **`pkg/run`**：MCP 段异步化、并行连接、按配置顺序注册（§6.1、§6.3）
4. **`pkg/run`**：首轮落定点 `WaitMCPReady` + 中断入口（§6.2）
5. **`pkg/tui`**：实时状态行 + 失败记录 + Esc 优先级（§6.7）
6. **`pkg/gateway` / `frontend`**：同一份状态的 Web 呈现（§6.8）

---

## 6. 详细设计

### 6.1 `Runner`：MCP 段异步化

#### 6.1.1 新增内部状态

```go
// mcpLoad 是一次在途的 MCP 段加载。fingerprint 让一次热重载能认出
// "同一份清单已经在连了"，从而既不重复拉起子进程，也不需要等它。
type mcpLoad struct {
    fingerprint string
    done        chan struct{}
    cancel      context.CancelFunc // Esc / 关闭会话时中断尚未落定的连接
}
```

`Runner` 增加字段 `mcpLoad *mcpLoad`，与 `mcpSeg` / `mcpReg` 一样由 `r.mu.load` 守护（`runnerLocks.load` 的文档注释同步补上这一项）。

#### 6.1.2 `loadLocked` 的改动

`pkg/run/runner.go:1130-1131` 现在是：

```go
baseCtx := context.Background()
mcpTools := r.loadMCPSegmentLocked(baseCtx, a)
```

改为：

```go
mcpTools := r.startOrReplayMCPSegmentLocked(a)
```

```go
// startOrReplayMCPSegmentLocked 决定这次 Load 对 MCP 段做什么：
//   - 指纹未变且已有缓存段 → 同步回放，不碰任何服务器（与今天一致）
//   - 指纹未变且有同指纹的加载在途 → 什么都不做，在途那次完成时注册进 r.main
//   - 其余 → 关掉旧 registry，起一次后台加载
// 返回值只用于日志，后台路径返回 0。
func (r *Runner) startOrReplayMCPSegmentLocked(a *agent.Agent) int
```

热路径（配置热重载、`/model` 之类引发的重新 Load）落在第一条分支上，行为完全不变 —— **重载不会因为本次改动而重新拉起 MCP 子进程**，那会既慢又打掉缓存。

#### 6.1.3 后台加载

```go
func (r *Runner) connectMCPSegment(ld *mcpLoad, servers []appcfg.MCPServerConfig, dirs []string)
```

两个阶段，**第一阶段不持任何锁**：

1. **连接阶段（不持锁、并行）**：每台服务器一个 goroutine，各自带 per-server deadline（§6.5）和 `ld` 的可取消上下文，做 `mcp.Start` + `ListToolMetas`。每台的状态迁移（connecting → connected / failed / cancelled）即时写进 registry（§6.6），界面因此能逐台看到进展。
2. **注册阶段（持 `r.mu.load` 写锁）**：
   - 若 `r.mcpLoad != ld`（期间又发生了一次指纹不同的 Load），把本次连出来的 session 全部 `Close()` 后返回 —— 结果作废，子进程不泄漏。
   - 否则**按 `servers` 的配置顺序**（不是完成顺序）把工具注册进 `r.main` 和 `r.tools`，逻辑与今天 `loadMCPSegmentLocked` 主体逐行一致（`newMCPDelegatingTool` → `r.tools.Register` → `RegisterToolMeta` → `registerMCPDiscoveryTools`）。
   - 写 `r.mcpSeg`，置 `r.mcpLoad = nil`，释放锁，`close(ld.done)`。
   - 锁外调用 `r.RefreshFilesystemPolicy()`，与 `Load()` 的收尾一致。
   - 打一条 `slog.Info("agent mcp tools", "mcp", n, "elapsed", …)`。今天 `loadLocked` 末尾那条 `agent tools ... mcp=N` 里的 `mcp` 计数必须挪到这里，否则它会永远打印 0，变成误导性日志。

**为什么注册必须持写锁**：`agent.Agent.AddTool`（`pkg/agent/agent.go:127`）是裸 slice append，没有任何同步。`Runner.LoadedTools()` 已经在 `r.mu.load.RLock()` 下读它。持写锁注册 + 落定点保证"注册完成前不会有 run 读工具表"，两者合起来让这里**结构上**不可能有竞态，而不是靠注释约定。

**为什么注册进 `r.main` 而不是捕获的 `a`**：`loadLocked` 在末尾才 `r.main = a`，而后台 goroutine 只能在 `Load()` 释放锁之后才拿到写锁，那时 `r.main` 必然已经是 `a`。用 `r.main` 让"注册到当前生效的 agent"成为不变式，而不是依赖两段代码的时序巧合。

### 6.2 落定点：`WaitMCPReady`

```go
// WaitMCPReady 阻塞到本 runtime 的 MCP 段落定（全部就绪、失败或被中断）。
//
// 这是"连接异步、工具表按会话冻结"的那个同步点：MCP 工具位于提示词前缀里的
// 工具表，会话中途改写工具表会作废整段缓存前缀，所以第一次 LLM 请求之前必须
// 等它落定。连接本身有 per-server 上界，用户还可以随时中断，因此这里的等待
// 既有界也可跳过。
func (r *Runner) WaitMCPReady(ctx context.Context) error

// CancelMCPStartup 中断尚未落定的 MCP 连接：已就绪的保留，其余标记 cancelled，
// 工具表就以此刻的集合落定。这是"不等它了"这个用户动作的唯一入口。
func (r *Runner) CancelMCPStartup()
```

`WaitMCPReady` 循环读 `r.mcpLoad`（RLock），nil 则返回；否则 select `<-ld.done` / `<-ctx.Done()`。循环覆盖"等的那次刚完成、又起了新的一次"这种交叉。

**调用点：`Runner.RunContent` 的最开头**（`pkg/run/runner.go:1256` 之前）。这是全仓库唯一的主 agent 执行入口，已核实所有 surface 都汇流到它：

```
TUI     ChatSession.dispatchUserTurnContent → process.RunExecutor → run.Run(Options)
Gateway turn.Submit → 同一个 RunExecutor    → run.Run(Options)
one-shot / worker                           → run.Run(Options)
                                                   ↓
                                     run.RunPipeline (pkg/run/run.go:241)
                                                   ↓
                              Runner.RunContent / Runner.Run → RunContent
```

TUI 额外在 `dispatchUserTurnContent` 调执行器之前也等一次，这样等待发生在能画状态行的位置；`RunContent` 里那次保留，作为所有 surface 的兜底。

子代理（`subagent_run` / `subagent_fanout` / fork）都在某一轮之内派生，落定点已经过了；派生出独立 `Runner` 的路径（`Factory.NewRunner` 带 `ownerMCPServers()`）各自有自己的落定点，语义一致。

### 6.3 并行连接与确定性注册顺序

连接并行，**注册严格按 `r.MCPServers` 的下标顺序**。

这是缓存约束的直接推论：`agent.tools` 是 slice，注册顺序就是请求里 `tools` 数组的顺序。若按"谁先连上谁先注册"，同一份配置在两次会话里会产生不同的工具数组顺序，前缀逐字节不同，整段缓存作废。**并行只允许出现在"连接"这一步，不允许渗进"注册"这一步。**

并发度不设上限（服务器数量本就是个位数，且每台都有独立超时）。

### 6.4 stdio 子进程生命周期归 forebrain 所有

`pkg/mcp/bridge.go` 的 stdio 分支：

```go
c := exec.Command(cmd, srv.Args...)          // 今天
```

改为由 forebrain 持有一个与 **session 生命周期**绑定（而不是与连接超时绑定）的 cancel：

```go
procCtx, cancelProc := context.WithCancel(context.WithoutCancel(ctx))
c := exec.CommandContext(procCtx, cmd, srv.Args...)
```

- `Session` 增加 `cancelProc` 字段，`Session.Close()` 在 SDK 的 `Close()` 之后调用它。
- `Connect` 返回错误的每条路径都先 `cancelProc()` 再 return。

要点：

- **不能**把 cancel 绑在启动超时的 ctx 上 —— 那样连接成功之后一到超时时间就会把服务器杀掉。`context.WithoutCancel(ctx)` 正是为了斩断这层继承。
- 这不是给 §3.3 里 SDK 那条分支打补丁，而是把"谁拉起谁负责回收"落到 forebrain 自己手里。SDK 之后怎么改都不会再让 forebrain 漏进程。
- `exec.CommandContext` 默认 Cancel 是 SIGKILL；它跑在 SDK 的优雅关闭（关 stdin → 5s → SIGTERM → kill）之后，进程通常已退出，是 no-op。

### 6.5 每服务器启动超时与 `required`

`pkg/config/agents.go` 的 `MCPServerConfig` 增加两个字段：

```go
// StartupTimeout 是这台服务器从拉起到首次 tools/list 返回的时间上界，单位秒。
// 0 表示用默认值。与 HookCommand.Timeout 同为秒的浮点数，保持一致。
StartupTimeout float64 `yaml:"startup_timeout,omitempty" json:"startup_timeout,omitempty"`

// Required 为真时，这台服务器不可跳过：首轮一直等到它落定，Esc 也不跳过它；
// 在 forebrain -p / gateway 这类无人值守场景下，它启动失败直接让会话初始化报错，
// 而不是静默少一批工具。默认 false。
Required bool `yaml:"required,omitempty" json:"required,omitempty"`
```

默认启动超时：**30 秒**，定义为 `pkg/mcp` 的导出常量。覆盖 `mcp.Start` + `ListToolMetas` 两步合计 —— 对使用者来说"就绪"就是"工具列出来了"。工具调用本身的超时是另一件事，不在本期。

超时命中时：该服务器标记 failed（`context deadline exceeded`），子进程被 §6.4 的 cancel 回收，**其余服务器不受影响**，落定照常。

`required` 的失败处理：交互式 TUI 里它和普通失败一样进会话记录（用户可以改配置重开），差别只在"Esc 不跳过它"；非交互式入口（`forebrain -p`、gateway 建会话）则把所有 required 失败合并成一条错误返回，一次报全而不是报第一条。

### 6.6 `mcp.Registry` 四态化 + 变更订阅

把 §3.4 的死字段用起来，让 registry 成为 MCP 连接状态的**唯一事实来源**，而不是另起一套并行状态。

新增（`pkg/mcp/registry.go`）：

```go
// MarkConnecting 记录一台已经开始拉起、尚未握手完成的服务器。
func (r *Registry) MarkConnecting(name, transport, url string)

// MarkFailed 记录一台启动失败的服务器，errMsg 是底层报错原文。
// authStatus 让"需要重新授权"与"进程起不来"在界面上是两件事。
func (r *Registry) MarkFailed(name, transport, url, errMsg string, authStatus AuthStatus)

// MarkCancelled 记录一台被用户中断、本会话不再尝试的服务器。
func (r *Registry) MarkCancelled(name, transport, url string)

// Subscribe 在 registry 内容变化时回调；返回取消函数。
// 订阅时立刻回调一次当前快照，所以订阅者不会漏掉订阅之前已经发生的迁移
// —— 这是必需的：MCP 连接在 TUI 建立订阅之前就已经开始了。
func (r *Registry) Subscribe(fn func([]ServerRecord)) (cancel func())
```

`ConnStatus` 补一个 `ConnStatusCancelled`。三个 Mark 都不携带 session，所以同一套方法对 owner registry 和 `GlobalRegistry()` 镜像都适用，不需要 `*Mirror` 变体。

失败到 `AuthStatus` 的映射复用既有取值：需要登录/令牌过期映射到 `AuthStatusNeedsAuth`，其余映射到 `AuthStatusFailed`。

`Runner` 暴露：

```go
func (r *Runner) MCPStatusSnapshot() []mcp.ServerRecord
func (r *Runner) ObserveMCPStatus(fn func([]mcp.ServerRecord)) (cancel func())
```

连带修好：`/mcp`、`/status`、`turn.MCPConfiguredConnected` 从此能说清"连接中 3 台 / 已连 1 台 / 失败 1 台（原因）"，而不是只能说"配置 5 台、连上 1 台"。

### 6.7 TUI 呈现

#### 6.7.1 实时状态行（复用既有组件，不新增控件）

forebrain 已经有这条线：`Renderer.StartTimedTransientStatus(formatter)`（`render.go:661`）驱动 spinner + 秒数，`workingStatusFormatter`（`run.go:3345`）就是它的一个 formatter，渲染成 `⠋ Working (12s • esc to interrupt) · …`。

MCP 启动只是**同一条线的另一个 formatter**：

```
单台：  ⠋ 拉起 MCP 服务器 caveman-shrink（12s • esc 跳过）
多台：  ⠙ 拉起 MCP 服务器（1/3）：alpha、beta（4s • esc 跳过）
```

规则：

- 只列仍在连接中的服务器名，最多 3 个，超出补省略号；括号里的分数是"已落定/总数"。
- 一台也没有配置 MCP 服务器时，不出现这条线。
- 全部落定后这条线消失。成功不留痕（状态行本来就是瞬态的），这正是它比卡片合适的地方。
- 文案一句话，不用 dim/低亮度，不使用粉色/品红。

驱动方式：`tui.Run` 进主循环前 `opts.Session.ObserveMCPStatus(...)`，回调里 `notifyQ.Push(MCPStartupMsg{...})` —— 与 `PrependUINotify` 同一条无损入队路径。因为 `Subscribe` 订阅即回放当前快照，晚于连接开始建立订阅也不会丢状态。

#### 6.7.2 通知消息

`pkg/tui/notify.go`：

```go
// MCPStartupMsg 是会话 MCP 连接状态的一次快照，驱动实时状态行。
// Done 为真表示本轮启动已落定，状态行收起。
type MCPStartupMsg struct {
    Servers []MCPStartupServer // 按配置顺序
    Done    bool
}

type MCPStartupServer struct {
    Name  string
    State string // connecting | connected | failed | cancelled
    Tools int
    Err   string // 底层报错原文，不加工
}
```

#### 6.7.3 失败进会话记录

状态行是瞬态的，失败不能只活在瞬态里。每台失败的服务器在落定时向会话记录追加一条 `FrameError`：

```
mcp caveman-shrink
calling "initialize": EOF
```

标题是服务器名，正文是**底层报错原文**，不套自编解释、不加建议。这是 §3.1 那个"失败只写日志"被彻底堵死的地方。被中断（cancelled）的服务器不写记录 —— 那是用户自己的决定，状态行上他已经看见了。

#### 6.7.4 Esc 的优先级

Esc 在 TUI 里已经有一条明确的优先级链（`run.go:3542` 起：roster 失焦 → 退出子代理视图 → 撤回提交 → 取消运行）。MCP 启动插在**"取消运行"之前、"撤回提交"之后**：没有运行在跑时，Esc 中断 MCP 启动；有运行在跑时，Esc 还是取消运行（那时启动早已落定）。

`required: true` 的服务器不被 Esc 跳过：状态行会继续只显示它，直到它落定或超时。

#### 6.7.5 首轮撞上未落定

组合器全程可用。用户在启动期间提交，提交被**保留在既有的回合输入队列里**，状态行继续走，落定后自动发出 —— 不丢输入、不弹新控件。forebrain 已经有这套机制（`deferQueueAutosendUntilSelectionApplied` / `resumeQueueAutosend`，用于"下一轮要用的设置还在选"），MCP 启动是同一类"下一轮的前置条件还没定"。

想立刻发的人按 Esc：启动中断，工具表就以此刻的集合落定，排队的输入马上发出。

#### 6.7.6 SessionStart 钩子（§3.5）

复用同一条实时状态行，换个 formatter：

```
⠸ 执行 SessionStart 钩子 <name>（3s • esc 跳过）
```

失败同样写一条 `FrameError`，正文是钩子的原始 stderr。

### 6.8 Gateway / Web

Runner 层的状态是 surface 无关的。Gateway 走既有 session 事件流发同一份快照，前端渲染成自己的实时状态条 + 失败记录。TUI 是语义标准，Gateway 复用底层引擎，不另写一套判断。

Gateway 自身启动同样受益：`pkg/gateway/server.go:1641` 的 `Runner.Load()` 不再串行等 MCP，服务可以先起来。

### 6.9 其余 `Load()` 调用点

| 调用点 | 影响 |
|---|---|
| `pkg/process/open.go:300` | 启动不再等 MCP（本方案主目标） |
| `pkg/process/agent_switch.go:90` | 切主 agent 不再卡住界面 |
| `pkg/process/runner_pool.go:292` | 项目 runner 池同理 |
| `pkg/process/config_reload.go:234` | 指纹不变 → 走同步回放分支，行为完全不变 |
| `pkg/gateway/server.go:1641` | 同上 |

---

## 7. 对提示词缓存命中率的影响（G5）

### 7.1 结论

**不变。**第一次 LLM 请求看到的 `tools` 数组与今天逐字节相同，且整个会话不再变化。

### 7.2 论证

1. **内容相同**：后台注册的工具，其构造代码与今天 `loadMCPSegmentLocked` 逐行一致（同样的 `BuildToolName`、`CapDescription`、schema 归一化、`inferMCPToolMeta`）。
2. **顺序相同**：§6.3 强制按配置顺序注册；今天也是按配置顺序。
3. **位置相同**：今天 MCP 工具在 `loadLocked` 里最后注册，其后没有任何工具注册（核对过 `runner.go:1131` 之后只有 `svc.Refresh()`、telemetry、`SetTracer`、`r.main = a`）。改后 MCP 工具仍然最后到。技能中途激活注册的工具发生在某一轮之内，在落定点之后，两种实现下都排在 MCP 之后。
4. **时机相同**：落定点保证注册完成早于第一次请求；落定之后工具表在本会话内不再变化。**晚到的服务器不会被塞进已经开跑的会话**，这是本方案与"热插工具"的根本分野。

### 7.3 新引入的两个变量

**超时**：会让"慢但最终能连上的服务器"在**跨会话**层面变得不确定（A 会话 28s 连上，B 会话 31s 超时），B 要重建一次缓存。但会话内前缀依然完全冻结；而今天的行为（无限期阻塞、用户 Ctrl+C 之后重开）跨会话同样不确定且代价更大。

**Esc 跳过**：用户按了 Esc，本会话的工具表就少一台服务器。这是**用户的显式选择**，而且状态行已经写明他在跳过谁；跳过之后这个会话的前缀依然全程冻结。

两者都不改变"会话内前缀不变"这条 —— 缓存命中率的主战场不受影响。结论：**严格改善，不是回退。**

### 7.4 守卫

1. **字节级 golden**：用假 MCP server 构造 N 台服务器，分别走同步（旧行为）与异步（新行为）两条路径，断言渲染出的 `tools` JSON 逐字节相等。
2. **乱序完成测试**：让假服务器以与配置相反的顺序完成握手，断言 `tools` 数组顺序仍按配置顺序，且与 golden 一致。
3. **冻结测试**：一台服务器在落定之后才连上，断言它的工具**没有**进入本会话的工具表。
4. **实测**：按既定口径，只在 DeepSeek 与 OpenAI 上取改前/改后的 `CacheRead / (CacheRead + CacheCreation + Input)`，同一段脚本化对话，要求改后 ≥ 改前。

---

## 8. 明确不做的事

- **不做**"先不带 MCP 工具开跑，连上再热插工具"。理由见 §4.2。
- **不做**失败服务器的自动重试/退避。失败原因通常是配置错误（本例的 `caveman-shrink` 根本不是 MCP server），重试只是把几十秒变成几分钟。用户看到报错原文后自己改配置，下个会话生效。
- **不做**工具目录的落盘快照。它能让稳态下首轮零等待，但引入一份新的本地状态和"服务器改了工具要下个会话才反映"的滞后，收益不抵复杂度；等实测确认首轮等待真的碍事再说。
- **不做**兼容层或数据迁移。`startup_timeout` / `required` 都是新增可选字段，不填走默认。
- **不读**任何外部工具的配置文件（`.mcp.json` 等），维持现状。

---

## 9. 任务拆解

| # | 任务 | 文件 | 完成判据 |
|---|---|---|---|
| P1 | `mcp.Registry` 四态化 + `Subscribe` | `pkg/mcp/registry.go` | `ConnStatusConnecting`/`Error`/新增 `Cancelled` 有写入方；订阅即回放当前快照；并发订阅/取消有单测 |
| P2 | stdio 子进程生命周期归属 | `pkg/mcp/bridge.go` | `exec.CommandContext` + session 绑定 cancel；连接失败后查不到残留子进程的测试 |
| P3 | `startup_timeout` + `required` | `pkg/config/agents.go`、`pkg/mcp/bridge.go` | 可配；默认 30s 常量；假服务器"永不回 initialize"时按时返回 `context deadline exceeded` |
| P4 | MCP 段异步化 + 并行 + 顺序注册 | `pkg/run/runner.go` | `startOrReplayMCPSegmentLocked`、`connectMCPSegment`；重载走回放分支不重拉子进程；乱序完成顺序测试通过 |
| P5 | `WaitMCPReady` + `CancelMCPStartup` | `pkg/run/runner.go`、`pkg/tui/chat_surface.go` | `RunContent` 首行落定；§7.4 的 golden 与冻结测试通过 |
| P6 | TUI 实时状态行（MCP + SessionStart 钩子） | `pkg/tui/run.go`、`notify.go`、`chat_session.go` | 单台/多台两种文案；秒数走既有 spinner；落定后收起 |
| P7 | Esc 优先级 + 输入排队 | `pkg/tui/run.go` | Esc 在无运行时中断启动、`required` 不被跳过；启动期间提交不丢、落定后自动发出 |
| P8 | 失败进会话记录 | `pkg/tui/run.go` | 每台失败一条 `FrameError`，正文是原始报错；cancelled 不写 |
| P9 | `/mcp`、`/status` 四态 | `pkg/turn/mcp_format.go` | 显示"连接中/已连/失败+原因/已跳过" |
| P10 | 非交互式入口的 `required` 硬失败 | `pkg/process`、`pkg/gateway` | required 服务器失败时一次报全部失败原因 |
| P11 | Gateway + Web 同语义 | `pkg/gateway`、`frontend` | Web 上能看到与 TUI 相同的启动状态与失败记录 |
| P12 | 真机验收 | — | §10.2 |

依赖：P1 → P4、P9、P11；P2、P3 → P4；P4 → P5 → P6 → P7、P8；P3 → P10。
P1/P2/P3 相互独立，可先并行落地。

---

## 10. 验证

### 10.1 自动化

- `pkg/mcp`：四态迁移、订阅回放与取消、超时返回、连接失败后无残留子进程。
- `pkg/run`：异步加载完成后工具已注册；落定点在注册完成前不放行；乱序完成仍按配置顺序注册；**落定后才连上的服务器不进本会话工具表**；指纹不变的重载不触发新连接；在途加载期间再次 Load 不重复拉起；作废的加载会 Close 自己的 session；`CancelMCPStartup` 后已就绪的保留、其余标 cancelled。
- **缓存 golden**：同步路径与异步路径渲染出的 `tools` JSON 逐字节相等（§7.4）。
- `pkg/tui`：状态行单台/多台文案；失败记录正文为原始报错；Esc 优先级；启动期间提交不丢。
- 全量 `go test ./...` + `go vet ./...`。

### 10.2 真机（`run-forebrain` 技能驱动真实 TUI）

模型行为与交互类改动必须在真实 TUI 里验过，不能只靠单测和假 provider。

1. **本 bug 的原始场景**：保留 `.forebrain/mcp_servers.yaml` 里的 `caveman-shrink`，启动 forebrain。
   - 期望：首帧 < 1s 出现；状态行显示 `拉起 MCP 服务器 caveman-shrink（Ns • esc 跳过）`；握手 EOF 时立即落定，会话记录里出现 `mcp caveman-shrink / calling "initialize": EOF`。
   - 对照：从日志量出 `opening chat session` → 首帧的间隔，与当前 70s 对比。
2. **健康服务器**：配一台真能跑的 stdio MCP server，确认首帧不等它、状态行走完即收起、`/mcp` 列得出工具、模型调得动。
3. **首轮撞上未落定**：配一台故意慢启动的服务器，启动后立刻敲消息回车。期望输入被保留、状态行继续、落定后自动发出。
4. **Esc 跳过**：同上场景按 Esc。期望启动中断、排队的输入立刻发出、本会话没有该服务器的工具、`/mcp` 显示"已跳过"。
5. **多服务器并行**：三台各 ~3s 的服务器，总落定时间 ≈ 3s 而非 9s；状态行显示 `（1/3）：…` 形态。
6. **永不回应**：配一台 `sleep infinity` 的假服务器，确认 30s 后按时失败、forebrain 全程可用、`ps` 里没有残留子进程。
7. **required**：把慢服务器标 `required: true`，确认 Esc 跳不过它；再用 `forebrain -p` 跑一次，确认它失败时直接报错退出。

---

## 11. 风险

| 风险 | 缓解 |
|---|---|
| 后台注册与 `agent.AddTool` 竞态（后者是裸 slice append） | 注册持 `r.mu.load` 写锁；落定点保证注册前无 run 读工具表。两者合起来是结构性保证，附带 `-race` 测试 |
| 首轮等待被误认为"卡住了" | 状态行写清在等谁、等了多久、Esc 能跳过；输入不丢 |
| 超时误杀慢但正常的服务器 | 默认 30s；per-server `startup_timeout` 可调；失败原文里能看出是 deadline |
| 用户习惯性按 Esc，导致工具表悄悄少了一台 | 状态行在被按之前就一直写着服务器名；跳过后 `/mcp` 明确显示"已跳过"，不是"未配置" |
| 在途加载期间发生热重载 | 指纹相同 → 复用在途那次；指纹不同 → 旧的作废并 Close 自己的 session，靠 `r.mcpLoad != ld` 的身份判断，不靠时序 |
