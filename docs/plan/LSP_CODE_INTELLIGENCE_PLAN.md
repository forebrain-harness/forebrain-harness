# LSP 代码智能接入方案：给 forebrain 加上语言服务器的语义与功能

> 日期：2026-09-29
> 状态：待评审（本文件只写方案，不含任何代码改动）
> 基线：`main` / `6305ea9`
> 范围：新增 `pkg/lsp`；改动 `pkg/config`、`pkg/event`、`pkg/tool`、`pkg/safety`、`pkg/run`、`pkg/turn`、`pkg/process`、`pkg/tui`、`pkg/gateway`、`frontend`、`pkg/migrate`、`cmd/forebrain`、`pkg/architecture`
> 对标：Claude Code 的 code intelligence 插件（`gopls-lsp`、`jdtls-lsp`、`rust-analyzer-lsp` 等）、它的 `LSP` 工具、编辑后诊断，以及 **LSP plugin recommendation** 对话框
> 必须支持的语言（需求原文）：Java、Go、Rust、C、C++、Kotlin、Swift、Scala、C#、TypeScript、PHP、Python 等主流语言，且不限于此

---

## 0. 一页结论

| # | 决策 | 要点 |
|---|---|---|
| D1 | **形态：内置语言服务器目录 + 配置，而不是插件市场** | forebrain 是单一二进制，没有插件系统。把「哪个命令启动哪个服务器、处理哪些扩展名、根目录怎么找、怎么安装」做成 `go:embed` 的目录（catalog），首发覆盖需求点名的 12 门语言，随后扩到 20+ 门。任何目录里没有的语言都能用一个 `lsp.servers.<name>` 自定义条目接入，字段与 Claude Code 的 `.lsp.json` 一一可映射，`/migrate` 可直接导入 |
| D2 | **两种能力** | ① **编辑后诊断**：`write_file` / `edit_file` / `apply_patch` 成功后，把「这次编辑新引入的」错误和警告附在工具结果里；服务器较晚才给出的诊断（`cargo check`、Gradle 编译）在下一次模型请求时以提醒注入。② **只读 `lsp` 工具**：定义、声明、类型定义、实现、引用、悬停类型、文档符号、工作区符号、调用层级、类型层级、当前诊断 |
| D3 | **服务器进程由 forebrain 托管** | 懒启动；按「(服务器, 工作区根)」一个实例，进程级池共享；空闲回收；崩溃指数退避重启；关闭时清理**整棵进程树**；**永远不在 `Runner.Load` 的关键路径上启动**（MCP 启动卡死 70 秒的教训） |
| D4 | **安全闸门** | 只在**受信任项目**启动；目录服务器需要用户**显式启用一次**（推荐框一键完成）；项目级条目走与 MCP 相同的**逐条同意 + 指纹**；工具只读，`workspace/applyEdit` 一律拒绝；权限规范名 `LSP`，`Read(...)` 路径规则同样约束它 |
| D5 | **prompt 缓存不动** | `lsp` 工具是否注册在会话冻结时决定、描述和 schema 是编译期常量、不改系统提示；会话中途启用或重启服务器只影响诊断，不动工具表和前缀 |
| D6 | **分层不破** | port 接口定义在 `pkg/tool`，DTO 在 `pkg/event`，实现在新包 `pkg/lsp`（Layer 2），只有组合根 `pkg/process` 导入它；`run` / `tool` / `tui` / `gateway` / `migrate` 的 fan-out **一个都不涨** |
| D7 | **推荐（截图那个框）** | 本会话第一次编辑某语言文件时，若该语言有目录服务器且未启用：弹「LSP recommendation」——启用 / 安装并启用 / 暂不 / 此服务器永不 / 关闭全部推荐；30 秒无操作计一次忽略，累计 5 次自动关闭。**比 Claude Code 多一步：接受后诊断在本会话立即生效，不用重启** |
| D8 | **分四期交付** | P0 运行时底座 → P1 模型能力（工具 + 诊断）→ P2 产品化（推荐、`/lsp`、Web、CLI、迁移、项目级配置）→ P3 进阶（长驻沙箱、rename 预览、诊断型服务器、文件监听） |

§14 列出需要拍板的 7 个决策及推荐选项。

---

## 1. 目标、非目标与完成定义

### 1.1 用户可见的产品结果

1. **编辑后诊断**：agent 改 `invoice.go` 引入了类型错误，编辑卡片下出现一行 `Found 2 new diagnostic issues in 1 file`（点击展开）；模型在同一个工具结果里拿到明细，不跑 `go build` 就能改正。
2. **按符号导航**：模型用 `lsp` 工具查定义、查引用、看类型，而不是 `grep` 文本（forebrain 目前没有 grep/glob 工具，代码导航全靠 shell，见 §2.1）。
3. **推荐对话框**：对应截图——首次编辑 `.go` 文件、`gopls` 已装但未启用时，弹出推荐；`gopls` 没装但 `go` 在 PATH 上时，提供「安装并启用」。
4. **`/lsp` 面板**（终端与 Web 同一套语义）：每个服务器的状态（未安装 / 可用 / 已启用 / 启动中 / 索引中 x% / 就绪 / 失败原因）、启用停用、重启、看日志、安装、重新开启推荐。
5. **`forebrain lsp doctor`**：逐项检查二进制、版本、根标记、环境变量，并做一次真实的 `initialize` 握手，打印修复建议。
6. **`/migrate`** 把 Claude Code 里已启用的 LSP 插件导入为 forebrain 的服务器配置。

### 1.2 语言覆盖

**必选（需求点名，P0 目录首发）**

| 语言 | 默认服务器 | 备选（目录内，低优先级） |
|---|---|---|
| Java | `jdtls`（Eclipse JDT LS） | — |
| Go | `gopls` | — |
| Rust | `rust-analyzer` | — |
| C | `clangd` | `ccls` |
| C++ | `clangd` | `ccls` |
| Kotlin | `kotlin-lsp`（JetBrains） | `kotlin-language-server`（fwcd，已停止维护） |
| Swift | `sourcekit-lsp` | — |
| Scala | `metals` | — |
| C# | `csharp-ls` | Roslyn LS、OmniSharp（P3） |
| TypeScript / JavaScript | `typescript-language-server` | `vtsls`；Deno 项目用 `deno lsp` |
| PHP | `intelephense` | `phpactor` |
| Python | `pyright` | `basedpyright`、`pylsp`、`ty` |

`clangd` 同时覆盖 Objective-C / Objective-C++ / CUDA。

**扩展（P2 起随目录发布，见 §5.3）**：Ruby、Lua、Dart/Flutter、Elixir、Zig、Haskell、OCaml、Bash、Vue、Svelte、Terraform、Clojure、Erlang、Nix、Gleam、YAML、Dockerfile、Protobuf 等；以及只提供诊断的服务器（ruff、ESLint、Biome）。

**「不限于」的兜底**：任意 LSP 服务器都能用自定义条目接入（§4.1），等价于在 Claude Code 里自写 `.lsp.json`。

### 1.3 非目标

- **不内置、不偷偷下载任何语言服务器二进制**（与 Claude Code 一致）。目录提供安装配方，执行必须由用户在界面上确认（§5.6）。
- 不做编辑器级功能：补全、语义高亮、inlay hints、格式化。格式化继续由项目自己的工具链负责。
- P0–P2 **不让 LSP 改文件**：rename / code action 在 P3 以「预览 → 走现有 `apply_patch` 与审批」的形态提供（§8.5）。
- 只用 stdio 传输（Claude Code 的 `transport: socket` 实际上也按 stdio 运行）。
- 通道会话（Telegram、Slack 等）没有项目根，不启动 LSP、不推荐。

### 1.4 完成定义

| 能力 | 验收方式（相对断言，不写死数量） |
|---|---|
| 12 门必选语言 | 每门语言一个 fixture 项目；集成测试断言 definition / references / hover / document_symbols 有结果，并完整走一遍「编辑引入错误 → 工具结果带诊断 → 修复 → 诊断消失」 |
| TUI 首帧不受影响 | 任何服务器的启动、探测都不在 `Runner.Load` 路径上；新增测试：一个 `initialize` 永不返回的假服务器不延迟首帧 |
| prompt 前缀字节稳定 | 会话中启用 / 停用 / 崩溃重启服务器、配置热重载，`LoadedTools()` 与系统提示的字节完全不变（新增测试） |
| 不留孤儿进程 | 退出、`/new`、Runner 被驱逐、forebrain 被 `kill -9` 之后，服务器**及其子进程**全部消失（Linux / macOS / Windows 各一个测试） |
| 包图 | `scripts/package-graph.sh` 生成的 `graph.json` 已提交；`pkg/architecture` 测试全绿；除 `process` 外 fan-out 不涨 |
| 安全 | 未受信任目录不启动、不推荐、不注册工具；项目条目未同意不启动；`workspace/applyEdit` 被拒并记日志 |

---

## 2. 现状与约束（全部来自代码实测）

### 2.1 仓库里没有任何 LSP 代码，也没有 grep/glob 工具

在非测试 Go 代码里搜索 `lsp`、`gopls`、`language server` 均零命中。注册的内建工具是 `read_file`、`write_file`、`edit_file`、`shell`、`retrieve_output`、`web_fetch`、`web_search` 以及会话/记忆/子代理类工具（`pkg/tool/registry.go:85 RegisterDefaultTools`）——**没有 grep、glob 这类搜索工具，找定义、找引用全靠 shell 里的 `grep`/`rg`**。这正是 LSP 收益最大的地方：按符号而不是按文本定位，且不会被同名标识符干扰。

### 2.2 工具表 = prompt 前缀，会话内冻结

`pkg/run/runner.go:1323`：「The agent is built once per session and never rebuilt from underneath a turn. Its tool definitions and skills catalog sit in the prompt prefix」。MCP 段为此专门做了指纹复用（`mcpSegmentFingerprint`），保证配置重载不动工具表字节。FOREBRAIN.md 的 house rule 要求 LLM 可见的 prompt 文本 provider-neutral 且字节稳定。

⇒ `lsp` 工具**是否注册**必须在会话冻结时决定；描述、schema 是常量；服务器的启停只能发生在工具之后，不能反过来改工具表。

### 2.3 包分层与 fan-out 棘轮：只有组合根能多一个导入

`pkg/architecture/cache_test.go` 有三道闸：`packageLayer`（:351，只能导入更低层）、`sameLayerEdges`（:526，同层边逐条白名单）、`fanOutBudgets`（:169，每个包的导入数上限，只降不升）。实测：

| 包 | 实测 fan-out | 上限 | 结论 |
|---|---|---|---|
| `tool` | 8 | 8 | 不能新增导入 ⇒ port 接口只能**定义在 `tool` 内** |
| `run` | 14 | 14（目标 12） | 不能新增导入 |
| `tui` | 19 | 19 | 不能新增导入 |
| `gateway` | 18 | 18 | 不能新增导入 |
| `migrate` | 8 | 8 | 不能新增导入 ⇒ Claude 插件的 `.lsp.json` 在 `migrate` 内解析，只写 `config` 结构 |
| `turn` | 10 | 12 | 有余量，但本方案不需要 |
| `process` | 18 | 18（§3.7 目标：不设上限） | 需要 +1（导入 `lsp`），与导入 `mcp` 的先例相同 |

⇒ 按测试失败信息本身给出的方向：「Reach a lower layer through an interface the consumer defines and process injects」。

### 2.4 子进程环境变量白名单会把工具链变量全部剥掉

`pkg/home/envguard.go:14` 的 `defaultAllow` 只有 `PATH HOME TMPDIR TMP TEMP LANG LC_ALL TZ USER SHELL TERM`；MCP 的 `inherit_parent_env` 分支（`pkg/mcp/stdio_secure.go:100`）也仍然过同一个白名单。语言服务器离不开 `GOPATH`/`GOFLAGS`/`GOPROXY`、`JAVA_HOME`、`CARGO_HOME`/`RUSTUP_HOME`、`VIRTUAL_ENV`、`DOTNET_ROOT`、`DEVELOPER_DIR` 等。

⇒ 目录条目按服务器声明 `env_passthrough`（§5.1），在白名单之上逐个放行，像密钥的变量名仍被 `looksSecretKey` 过滤。

### 2.5 MCP 子进程只杀直接子进程

`pkg/mcp/bridge.go:501` 用 `exec.CommandContext` 启动，没有进程组。语言服务器几乎都会再起子进程：`jdtls` 的 Python 启动器 → `java`；`typescript-language-server` → `tsserver`；`rust-analyzer` → `cargo check` → `rustc`；`gopls` → `go list`。

⇒ LSP 必须用进程组（Unix）/ Job Object（Windows）清理整棵树（§6.8）。

### 2.6 「读文件观察者」已经存在，但生产代码从未接线

`pkg/tool/state.go:1833 SetReadObserver` 与 `pkg/tool/file_tools.go:117` 的调用点都在，生产代码里没有任何 `SetReadObserver` 调用（只有 `file_tools_test.go:179`）。

⇒ 可直接复用它做「已打开服务器的文档预打开（诊断基线）」；需要改成可挂多个观察者（fan-out），避免后来者覆盖。

### 2.7 项目级配置的信任 + 同意模型已经成熟

项目级 MCP：只读 `<project>/.forebrain/mcp_servers.yaml`，受信任且受版本控制的项目才生效，每个条目按指纹逐条同意（`pkg/mcp/project_consent.go`），启动时在信任提示之后询问（`cmd/forebrain/mcp_consent.go:20`），会话列表在 `pkg/process/open.go:255 ResolveSessionMCP` 冻结。

⇒ 项目级 LSP 条目照搬这套模型，独立的同意存储（§4.2）。

### 2.8 子 Runner 不会自动继承 `Deps` 的新字段

`pkg/run/factory.go:46 NewIsolatedRunner` 逐个列出要继承的字段（`MCPServers`、`MCPProject`、`ProjectRoot`…）。新增的 `CodeIntel` 若不加在这里，子代理和 fork 就没有 LSP——更糟的是 fork 子会话的工具表会和父会话不一样，破坏 fork 复用父前缀的缓存。

### 2.9 Gateway 按「(primary agent × 项目)」一个 Runner

`pkg/process/runner_pool.go` 的头注释：每个 Runner 挂着自己的 MCP 连接，空闲一段时间先释放连接、再按需驱逐整个 Runner（`:293 ReleaseIdleConnections`）。

⇒ LSP 实例池放在进程级 `Environment` 上、按工作区根共享；Runner 只持有引用，跟随同样的空闲节奏释放（§6.8）。

### 2.10 提醒注入已有现成通道

计划模式与 skill offer 都通过 `reminderAdoptionSink`（`pkg/run/config.go:996`）把提醒插到请求末尾并落进 transcript（`pkg/run/skills.go:539 wrapSkillOfferLLM`）。

⇒ 迟到诊断复用这条通道（§7.4），不发明新机制。

### 2.11 权限引擎按规范名分类工具

`safeReadOnlyTools`（`pkg/safety/engine.go:406`）、`isFilePathTool`（`engine.go:388`，只认 `Read/Write/Edit/MultiEdit`）、`canonicalToolAliases`（`pkg/safety/permission_rules.go:338`）。

⇒ 新增别名 `lsp → LSP`，并让 `Read(...)` 路径规则同样作用于 `LSP`（§8.4）。

### 2.12 Claude Code 的做法（官方文档实测）

- **配置**：插件里的 `.lsp.json`（或 `plugin.json` 的 `lspServers`），严格对象，字段：`command`（必填）、`extensionToLanguage`（必填）、`args`、`transport`（接受 `socket` 但实际按 stdio 运行）、`env`、`initializationOptions`、`settings`（经 `workspace/didChangeConfiguration` 下发）、`workspaceFolder`、`startupTimeout`（毫秒）、`shutdownTimeout`（毫秒）、`restartOnCrash`（默认 true）、`maxRestarts`、`diagnostics`（默认 true）。`${CLAUDE_PLUGIN_ROOT}`、`${CLAUDE_PLUGIN_DATA}`、`${CLAUDE_PROJECT_DIR}` 在 `command/args/env/workspaceFolder` 里展开。
- **官方插件**：`clangd-lsp`、`csharp-lsp`（`csharp-ls`）、`gopls-lsp`、`jdtls-lsp`、`kotlin-lsp`、`lua-lsp`、`php-lsp`（`intelephense`）、`pyright-lsp`（`pyright-langserver`）、`ruby-lsp`、`rust-analyzer-lsp`、`swift-lsp`（`sourcekit-lsp`）、`typescript-lsp`（`typescript-language-server`）。**没有 Scala**——forebrain 的目录要补上 `metals`。
- **能力**：每次编辑后自动报告错误与警告（界面显示 `Found N new diagnostic issues in M files`）；`LSP` 工具只读，支持跳定义、查引用、类型信息、文件符号、工作区符号、查实现、调用层级；权限与 `Read(path)` 共用规则格式。
- **启动**：服务器在第一次编辑匹配文件时启动；启动失败时对该文件的每次 LSP 调用返回错误。
- **推荐框**：二进制已在 PATH、对应插件未安装时，在某次编辑之后出现；多个市场都有时优先官方；每会话最多一次；选项「Yes, install / No, not now（Esc 同）/ Never for this plugin / Disable all LSP recommendations」；30 秒无操作自动关闭并计一次忽略，累计 5 次等同全部关闭；状态存在 `~/.claude.json` 的 `lspRecommendationDisabled`、`lspRecommendationIgnoredCount`、`lspRecommendationNeverPlugins`；**安装后需重启会话才生效**。

forebrain 在此基础上的改进：接受后**本会话立即获得诊断**；二进制未安装时可「安装并启用」；有 `/lsp` 面板与 `doctor`；同一文件可合并多个诊断型服务器；覆盖 Scala。

---

## 3. 总体架构

```
┌──────────────────────────── Layer 5 ────────────────────────────┐
│ pkg/tui      /lsp 面板 · 推荐浮层 · 编辑卡片诊断行 · lsp 工具卡片 │
│ pkg/gateway  /api/v1/lsp/* · WS 事件 ；frontend: LspView、推荐卡片 │
└───────────────▲─────────────────────────────────────────────────┘
                │ 只经 tool.CodeIntelControl（接口）与 event.LSP*（DTO）
┌─ Layer 4 ─────┴─────────────────────────────────────────────────┐
│ pkg/process  构造 lsp.Pool；解析生效服务器（全局 + 项目 + 同意 +   │
│              启用状态）；注入 run.Deps.CodeIntel；推荐决策落盘；关闭 │
└───────────────▲─────────────────────────────────────────────────┘
┌─ Layer 3 ─────┴─────────────────────────────────────────────────┐
│ pkg/run   CodeIntel → AgentToolRuntime；按冻结结果注册 lsp 工具；   │
│           迟到诊断提醒 wrapper；推荐事件 → RunEvent；Close/空闲释放 │
│ pkg/turn  /lsp 命令登记                                           │
└───────────────▲─────────────────────────────────────────────────┘
┌─ Layer 2 ─────┴─────────────────────────────────────────────────┐
│ pkg/tool   port：CodeIntelligence / CodeIntelControl；lsp 工具；    │
│            write/edit/apply_patch 之后的诊断钩子；读观察者 fan-out   │
│ pkg/lsp    jsonrpc · protocol · client · supervisor · docsync ·    │
│   (新)     diagnostics · position · root · catalog(embed) ·        │
│            detect · pool · manager · recommend · format            │
│ pkg/event  LSP DTO 与 RunEvent 类型                                │
│ pkg/safety LSP 规范名；Read 规则复用；(P3) 长驻进程沙箱            │
└───────────────▲─────────────────────────────────────────────────┘
┌─ Layer 1 ─────┴─────────────────────────────────────────────────┐
│ pkg/config  lsp 配置段、features.lsp                              │
└─────────────────────────────────────────────────────────────────┘
```

### 3.1 依赖变化（`pkg/architecture` 需同步改的表）

| 变化 | 说明 |
|---|---|
| `packageLayer` 加 `"lsp": 2` | 与 `mcp` 同层 |
| `sameLayerEdges` 加 `"lsp": {"event", "tool"}` | 实现 `tool` 的 port、使用 `event` 的 DTO；拓扑序变为 `safety < event < tool < {hook, mcp, skill, lsp} < …` |
| `fanOutBudgets` 加 `"lsp": {current: 4, target: 4}` | `config`、`home`、`event`、`tool` |
| `fanOutBudgets.process` 18 → 19 | 组合根导入 `lsp`，注释写明理由（与导入 `mcp` 的先例相同） |
| 其余包 fan-out 不变 | `run`/`tool`/`tui`/`gateway`/`turn`/`migrate` 都不新增导入 |
| `graph.json` 重新生成并提交 | CI 跑 `scripts/package-graph.sh` 比对 |
| `.gitmessage` 的 scope 列表加 `lsp` | 新包的提交用 `feat(lsp): …` |

### 3.2 接口（port）与 DTO

```go
// pkg/tool/code_intel.go —— 由消费方定义的接口，pkg/lsp 实现，pkg/process 注入。
type CodeIntelligence interface {
    // Handles reports whether an enabled server covers this file (no process start).
    Handles(absPath string) bool
    // Query runs one lsp-tool operation; it starts the server lazily.
    Query(ctx context.Context, q CodeIntelQuery) (CodeIntelResult, error)
    // DidWrite syncs files the agent just wrote and returns the problems the
    // write introduced, within the configured wait window.
    DidWrite(ctx context.Context, changes []FileChange) (DiagnosticsDelta, error)
    // DidRead opens the document on an already running server (diagnostics
    // baseline) and feeds the recommendation trigger. Never starts a server.
    DidRead(ctx context.Context, absPath string)
    // DidRunShell sweeps for files a shell command changed outside the tools.
    DidRunShell(ctx context.Context)
    // DrainLate returns diagnostics that arrived after the wait window for
    // files this agent session edited.
    DrainLate(agentSessionID string) []FileDiagnostics
}

// 面向界面（/lsp、推荐、Web API）的控制面，同样定义在 pkg/tool。
type CodeIntelControl interface {
    Snapshot() event.LSPSnapshot
    Subscribe(func(event.LSPSnapshot)) (cancel func())
    SetEnabled(serverID string, on bool) error
    Restart(serverID string) error
    Install(ctx context.Context, serverID string, progress func(string)) error
    DecideRecommendation(id string, choice event.LSPRecommendationChoice) error
    ResetRecommendations() error
}
```

DTO（`event.LSPServerStatus`、`event.LSPSnapshot`、`event.LSPRecommendation`、`event.LSPRecommendationChoice`、`event.LSPDiagnostic`）放在 `pkg/event/lsp.go`，因为 `tui`、`gateway`、`run`、`tool` 都已导入 `event`。新增 RunEvent 类型：`lsp_recommendation`、`lsp_status`。

### 3.3 `pkg/lsp` 内部布局

```
pkg/lsp/
  doc.go
  jsonrpc/        Content-Length 分帧、并发请求、服务器请求分派、取消
  protocol/       手写的 LSP 3.17 子集类型（含联合类型的自定义解码）
  client.go       一个服务器连接：initialize / shutdown / 请求 / 服务器请求
  supervisor.go   进程生命周期：启动、就绪、崩溃重启、优雅关闭
  procgroup_unix.go / procgroup_windows.go   进程树清理
  docsync.go      打开文档表、版本号、磁盘比对、LRU 关闭
  diagnostics.go  push/pull 统一存储、基线与增量、迟到队列
  position.go     UTF-8/16/32 换算、symbol 锚定
  uri.go          file URI 规范化（Windows 盘符、大小写不敏感文件系统、符号链接）
  root.go         根标记解析、monorepo、多根
  catalog/*.yaml  内置目录（go:embed）；catalog.go 解析与校验
  detect.go       二进制探测、版本探测、安装配方
  pool.go         进程级实例池：键、引用计数、上限、空闲回收
  manager.go      一个 Runner 的视图：实现 tool.CodeIntelligence
  control.go      实现 tool.CodeIntelControl
  recommend.go    推荐触发与决策存储
  format.go       给模型的结果格式化（稳定、紧凑）
  settings.go     config ⇄ 目录条目合并、workspace/configuration 应答
```

`pkg/lsp/jsonrpc` 等子目录在分层测试里按顶级目录名归入 `lsp`，无需单独登记。

---

## 4. 配置模型

### 4.1 全局：`~/.forebrain/forebrain.yaml`

```yaml
features:
  lsp: true                # 总开关，默认 true；false：不注册工具、不启动、不推荐

lsp:
  recommendations: true    # 推荐框，默认 true
  max_servers: 6           # 本进程同时存活的实例上限（gateway 为全进程）
  idle_timeout: 600        # 秒：无请求、无编辑多久后关闭实例
  request_timeout: 30      # 秒：单个工具请求上限（含等待索引）
  diagnostics:
    after_edit: true       # 编辑后诊断
    wait_ms: 2500          # 同步等待窗口
    min_severity: warning  # error | warning | information | hint
    max_per_file: 20
    max_files: 10
    late_delivery: true    # 迟到诊断在下一次模型请求时提醒
  servers:
    gopls:                 # 目录 id：只写要覆盖的字段
      settings:
        gopls: { staticcheck: true }
    mylang:                # 自定义服务器（目录里没有的语言）
      command: /opt/mylang/bin/mylang-ls
      args: ["--stdio"]
      extension_to_language: { ".ml2": "mylang" }
      root_markers: ["mylang.toml"]
```

**启用状态不写回 YAML**：推荐框「启用」、`/lsp` 里的开关写到 `<agentWorkspace>/state/lsp/enabled.json`，作为覆盖层。这与 MCP 禁用存储（`pkg/mcp/project_config.go` 的 `disabledStore`）的理由相同：改写用户 YAML 会丢注释，还可能改变项目同意指纹。生效顺序：`enabled.json` 覆盖 > YAML 的 `enabled` > 目录默认（目录条目默认**未启用**，自定义条目默认启用）。

服务器字段（与 Claude Code `.lsp.json` 对照）：

| 字段 | 类型 / 默认 | Claude Code 对应 | 说明 |
|---|---|---|---|
| `enabled` | bool；见上 | 插件是否启用 | |
| `command` | string；目录给出 | `command` | 不经 shell；可写绝对路径或 PATH 上的名字；支持 `~` |
| `args` | []string | `args` | |
| `extension_to_language` | map | `extensionToLanguage` | 键以 `.` 开头 |
| `filenames` | map | — | 精确文件名 → languageId（`Dockerfile`、`go.mod`） |
| `root_markers` | []string | — | 自文件所在目录向上找，止于项目根 |
| `workspace_folder` | string | `workspaceFolder` | 显式根，覆盖 `root_markers` |
| `env` | map | `env` | 支持 `${VAR}`；项目条目只能引用 `~/.forebrain/.env` |
| `env_passthrough` | []string；目录给出 | — | 在基础白名单之上放行的父进程变量名 |
| `initialization_options` | any | `initializationOptions` | |
| `settings` | any | `settings` | 既用于 `didChangeConfiguration`，也用于应答 `workspace/configuration` |
| `startup_timeout` | 秒；目录给出 | `startupTimeout`（毫秒） | 与 MCP 的 `startup_timeout` 一样用秒 |
| `shutdown_timeout` | 秒；5 | `shutdownTimeout`（毫秒） | 超时后杀进程树 |
| `restart_on_crash` | bool；true | `restartOnCrash` | |
| `max_restarts` | int；3 | `maxRestarts` | 10 分钟窗口内 |
| `diagnostics` | bool；true | `diagnostics` | false：不参与编辑后诊断 |
| `role` | `primary` \| `diagnostics` | — | `diagnostics` 型只贡献诊断（ruff、ESLint、Biome） |
| `priority` | int；目录给出 | — | 同一文件多个 primary 时的选择顺序 |
| `prewarm` | bool；false | — | 会话载入后在后台预启动（不阻塞 Load） |
| `sandbox` | `off`\|`auto`\|`required` | — | P3 生效，见 §8.6 |

### 4.2 项目级：`<project>/.forebrain/lsp_servers.yaml`

- 结构同全局的 `servers:`；只在**受信任且受版本控制**的项目生效（与项目级 MCP 同一判断）。
- 项目条目可以：覆盖目录服务器的 `args/env/initialization_options/settings/root_markers`；声明新服务器；请求启用某个目录服务器。
- **每个项目条目都要逐条同意**，指纹 = `command + args + env + initialization_options + settings + workspace_folder` 的 sha256；指纹变了重新询问。存储：`<agentWorkspace>/state/lsp/project_consent.json`，结构与 MCP 的相同。
  理由：`settings` 并不无害——`rust-analyzer` 的 `check.overrideCommand` / `cargo.buildScripts.overrideCommand`、pyright 的 `python.pythonPath`、gopls 的 `build.env` 都能让服务器执行任意程序。
- 同意入口：终端启动时紧跟 MCP 同意之后（照 `cmd/forebrain/mcp_consent.go` 的流程）；Web 在项目页（照 `/v1/projects/:id/mcp/consent`）。
- 明文密钥拒收（与项目级 MCP 相同）。
- 不原生读取其他 agent 的项目文件；导入是 `/migrate` 的职责（与 MCP 的约定一致）。

### 4.3 两件事、两种生命周期

| 什么 | 何时计算 | 能否会话中途变化 | 原因 |
|---|---|---|---|
| **服务器集合**（哪些服务器可用、各自配置） | 每次需要时由 config + 项目条目 + 同意 + `enabled.json` 计算 | **能**：推荐接受、`/lsp` 开关、配置热重载都立即生效 | 不进入 prompt 前缀 |
| **`lsp` 工具是否注册** | 由 `process` 在冻结会话上下文时计算，存进 `Deps.CodeIntelTool`，与 MCP 列表同一时机冻结；配置重载不重算 | **不能** | 工具表在 prompt 前缀里（§2.2） |

注册条件：`features.lsp && 启动项目受信任 && 冻结时生效集合里至少一个已启用的 primary 服务器`。不要求二进制已安装——缺失时调用返回可操作的错误（给出安装命令）。

配置热重载（`ConfigManager`）里 `lsp` 段变化时：池做一次对账——指纹变了的实例优雅关闭，下次需要时按新配置懒启动；工具表不动。

### 4.4 每个 primary agent 的状态目录

```
<agentWorkspace>/state/lsp/
  enabled.json             # 启用覆盖层
  recommendations.json     # {disabled, ignored_count, never:[ids]}
  project_consent.json     # 项目条目同意
  detect.json              # 二进制探测缓存（路径 + mtime + 版本）
  logs/<id>-<roothash>.log # 每实例日志，5 MB × 2 轮转
  cache/<id>/<roothash>/   # jdtls -data、intelephense storagePath 等
```

### 4.5 与 Claude Code `.lsp.json` 的映射（`/migrate` 用）

| Claude Code | forebrain | 转换 |
|---|---|---|
| 服务器名（键） | `lsp.servers.<name>` | 命令命中目录条目时只启用该目录 id，并把差异字段写成覆盖 |
| `command` / `args` / `env` / `workspaceFolder` | 同名 snake_case | `${CLAUDE_PLUGIN_ROOT}` → 插件缓存目录的绝对路径；`${CLAUDE_PROJECT_DIR}` → 迁移时丢弃该条目并报告（forebrain 以项目根解析） |
| `extensionToLanguage` | `extension_to_language` | 原样 |
| `initializationOptions` / `settings` | `initialization_options` / `settings` | 原样 |
| `startupTimeout` / `shutdownTimeout`（毫秒） | 秒 | ÷1000 |
| `restartOnCrash` / `maxRestarts` / `diagnostics` | 同名 snake_case | 原样 |
| `transport: socket` | — | 忽略（本来也按 stdio 运行） |
| `${user_config.*}` | — | 无法解析：跳过该条目并在报告里列出 |

---

## 5. 内置语言服务器目录

### 5.1 条目格式（`pkg/lsp/catalog/*.yaml`）

```yaml
id: gopls
display_name: gopls
languages: [go]
role: primary
priority: 100
command: gopls
args: ["serve"]
extension_to_language: { ".go": go }
filenames: { "go.mod": go.mod, "go.work": go.work }
root_markers: [go.work, go.mod]
env_passthrough: [GOPATH, GOROOT, GOBIN, GOFLAGS, GOPROXY, GOPRIVATE, GONOPROXY,
                  GONOSUMDB, GOTOOLCHAIN, GOWORK, CGO_ENABLED, CC, CXX,
                  PKG_CONFIG_PATH, HTTP_PROXY, HTTPS_PROXY, NO_PROXY]
detect:
  lookup: [gopls]
  extra_dirs: ["$GOBIN", "$GOPATH/bin", "~/go/bin"]
  version: [gopls, version]
install:
  - requires: go
    command: go install golang.org/x/tools/gopls@latest
startup_timeout: 60
readiness: progress        # progress | rust-analyzer-status | jdtls-status | none
did_save: true
auto_answers: {}           # window/showMessageRequest 的自动应答
conflicts: []              # 同扩展名冲突规则（§5.4）
project_writes: []         # 已知会写进项目目录的路径（doctor 与文档提示）
```

一个目录校验测试保证：每门必选语言都有条目；每个条目都有扩展名、命令、至少一条安装配方（按平台）；同一扩展名的多个 primary 必须有 `priority` 或 `conflicts` 规则。

### 5.2 必选语言明细

| 语言 | id / 命令 | 扩展名 → languageId | 根标记 | 安装配方 | 额外放行的环境变量 | 特殊处理 |
|---|---|---|---|---|---|---|
| **Go** | `gopls` / `gopls serve` | `.go→go`；`go.mod→go.mod`、`go.work→go.work` | `go.work`、`go.mod` | `go install golang.org/x/tools/gopls@latest` | 见上 | 多模块靠 `go.work` 或 workspaceFolders；`GOTOOLCHAIN=auto` 可能按 `go.mod` 的 `toolchain` 下载并运行工具链（§8.1） |
| **Rust** | `rust-analyzer` | `.rs→rust` | `Cargo.toml`（取项目内最上层含 `[workspace]` 的）、`rust-project.json` | `rustup component add rust-analyzer` | `CARGO_HOME RUSTUP_HOME RUSTUP_TOOLCHAIN RUSTFLAGS` | 默认设置 `cargo.targetDir: true`（用 `target/rust-analyzer`，**不和 agent 自己的 `cargo build` 抢 target 锁**）；保存触发 `cargo check`，诊断多为迟到型（§7.4）；就绪看 `experimental/serverStatus` 的 quiescent；build.rs 与过程宏会执行项目代码 |
| **C / C++**（及 ObjC、CUDA） | `clangd` / `clangd --background-index --header-insertion=never --log=error` | `.c→c`、`.h→c`（clangd 以编译命令为准）、`.cc .cpp .cxx .c++ .hpp .hh .hxx .ipp→cpp`、`.m→objective-c`、`.mm→objective-cpp`、`.cu .cuh→cuda-cpp` | `compile_commands.json`、`compile_flags.txt`、`.clangd`、`CMakeLists.txt`、`meson.build`、`Makefile` | Linux：发行版包 `clangd`；macOS：Xcode 自带（`xcrun --find clangd`）或 `brew install llvm`；Windows：LLVM 安装包 | `CPATH C_INCLUDE_PATH CPLUS_INCLUDE_PATH SDKROOT DEVELOPER_DIR` | 没有 `compile_commands.json` 时误报很多：doctor 与推荐文案提示 `cmake -DCMAKE_EXPORT_COMPILE_COMMANDS=ON` 或 `bear -- make`；后台索引写 `<root>/.cache/clangd/` |
| **Java** | `jdtls` / `jdtls -data <cache>/jdtls/<roothash>` | `.java→java` | `pom.xml`、`build.gradle(.kts)`、`settings.gradle(.kts)`、`mvnw`、`gradlew`、`.project` | macOS：`brew install jdtls`；其他平台：下载 Eclipse JDT LS 发行包，把 `bin/jdtls` 放进 PATH；需要较新的 JDK（新版 jdtls 要求 21+）与 python3 | `JAVA_HOME MAVEN_OPTS M2_HOME GRADLE_USER_HOME GRADLE_OPTS HTTP_PROXY HTTPS_PROXY NO_PROXY` | `-data` 必须在项目外；默认设置 `java.import.generatesMetadataFilesAtProjectRoot: false`（不往项目根写 `.project/.classpath/.settings`）；`startup_timeout: 180`；就绪看 `language/status` 的 `ServiceReady`；Maven/Gradle 导入会执行构建脚本且需要网络 |
| **Kotlin** | `kotlin-lsp` / `kotlin-lsp --stdio` | `.kt→kotlin`、`.kts→kotlin` | `settings.gradle(.kts)`、`build.gradle(.kts)`、`pom.xml` | `brew install JetBrains/utils/kotlin-lsp` 或 GitHub release 解压 | `JAVA_HOME GRADLE_USER_HOME GRADLE_OPTS` | 默认是 socket 模式，**必须带 `--stdio`**；需要 JDK；Gradle 导入慢，`startup_timeout: 180` |
| **Swift** | `sourcekit-lsp` / macOS 先试 `xcrun sourcekit-lsp`，否则 `sourcekit-lsp` | `.swift→swift`（C/ObjC 仍归 clangd） | `Package.swift`、`buildServer.json`、`compile_commands.json` | 随 Xcode 或 swift.org 工具链安装 | `DEVELOPER_DIR SDKROOT TOOLCHAINS` | 优先用 pull 诊断（`textDocument/diagnostic`）；SwiftPM 的索引写进 `.build/`；Xcode 工程没有 `buildServer.json`（xcode-build-server 生成）时只有单文件语义，doctor 给出提示 |
| **Scala** | `metals` | `.scala .sc .sbt .mill→scala` | `build.sbt`、`build.sc`、`build.mill`、`project/build.properties`、`.bsp/`、`project.scala` | `cs install metals`（Coursier） | `JAVA_HOME SBT_OPTS JAVA_OPTS COURSIER_CACHE HTTP_PROXY HTTPS_PROXY NO_PROXY` | 首次打开会用 `window/showMessageRequest` 询问是否导入构建：`auto_answers` 自动选「Import build」；`initialization_options: {statusBarProvider: log-message, isHttpEnabled: false}`；在项目根写 `.metals/ .bloop/ .bsp/` |
| **C#** | `csharp-ls` | `.cs→csharp`、`.csx→csharp` | `*.sln`、`*.slnx`、`*.csproj`、`global.json`、`Directory.Build.props` | `dotnet tool install --global csharp-ls` | `DOTNET_ROOT DOTNET_CLI_HOME NUGET_PACKAGES` | 需要 .NET SDK；未 `dotnet restore` 的项目诊断不全，doctor 提示；Roslyn LS 需要非标准的 `solution/open` 通知，放 P3 |
| **TypeScript / JavaScript** | `typescript-language-server` / `typescript-language-server --stdio` | `.ts .mts .cts→typescript`、`.tsx→typescriptreact`、`.js .mjs .cjs→javascript`、`.jsx→javascriptreact` | `tsconfig.json`、`jsconfig.json`、`package.json` | `npm install -g typescript-language-server typescript` | `NODE_PATH NODE_OPTIONS NPM_CONFIG_PREFIX` | 优先用项目 `node_modules/typescript`（`initialization_options.tsserver.path` 可覆盖）；tsconfig 的 `compilerOptions.plugins` 会加载 `node_modules` 里的代码；有 `deno.json(c)` 的根改用 `deno lsp`（§5.4） |
| **PHP** | `intelephense` / `intelephense --stdio` | `.php .phtml→php` | `composer.json` | `npm install -g intelephense` | — | `initialization_options.storagePath/globalStoragePath` 指向 cache 目录；部分高级功能需要 licence key，按服务器声明的 capability 自动降级；备选 `phpactor language-server` |
| **Python** | `pyright` / `pyright-langserver --stdio` | `.py .pyi→python` | `pyproject.toml`、`pyrightconfig.json`、`setup.py`、`setup.cfg`、`requirements.txt`、`Pipfile` | `npm install -g pyright` 或 `pipx install pyright` | `VIRTUAL_ENV CONDA_PREFIX PYTHONPATH` | 服务器会发 `workspace/configuration`（section `python`）：客户端按 `$VIRTUAL_ENV` → `<root>/.venv` → `<root>/venv` → PATH 上的 `python3` 探测解释器，回填 `python.pythonPath`；备选 `basedpyright-langserver --stdio`、`pylsp`、`ty server` |

### 5.3 扩展语言（P2 起）

| 语言 | 服务器 / 命令 | 安装 |
|---|---|---|
| Ruby | `ruby-lsp` | `gem install ruby-lsp` |
| Lua | `lua-language-server` | 包管理器 |
| Dart / Flutter | `dart language-server --protocol=lsp` | Dart/Flutter SDK 自带 |
| Elixir | ElixirLS / Expert | 各自发行包 |
| Zig | `zls` | 包管理器 |
| Haskell | `haskell-language-server-wrapper --lsp` | `ghcup install hls` |
| OCaml | `ocamllsp` | `opam install ocaml-lsp-server` |
| Bash | `bash-language-server start` | `npm install -g bash-language-server` |
| Vue | `vue-language-server --stdio` | `npm install -g @vue/language-server`（需与 TS 服务器协同） |
| Svelte | `svelteserver --stdio` | `npm install -g svelte-language-server` |
| Terraform | `terraform-ls serve` | HashiCorp 发行包 |
| Clojure | `clojure-lsp` | 包管理器 |
| Erlang | `elp server` | ELP 发行包 |
| Nix | `nixd` | 包管理器 |
| Gleam | `gleam lsp` | Gleam 自带 |
| YAML | `yaml-language-server --stdio` | npm |
| Dockerfile | `docker-langserver --stdio` | npm |
| 诊断型 | `ruff server`（Python）、`vscode-eslint-language-server --stdio`（JS/TS）、`biome lsp-proxy`（JS/TS/JSON） | 各自安装方式 |

扩展条目的命令行在实现时以 `forebrain lsp doctor` 的实测为准，并进 nightly 集成测试（§12）。

### 5.4 同一文件多个服务器

- **primary**：每个文件最多一个负责导航。选择顺序：项目条目覆盖 > 冲突规则 > `priority`。
- **冲突规则**示例：`deno` 条目声明 `conflicts: [{with: typescript-language-server, when_root_marker: [deno.json, deno.jsonc]}]`——根上有 `deno.json` 时 deno 胜出，否则 tsserver 胜出。
- **diagnostics 型**与 primary 并行运行，诊断合并，每条带来源（`[ruff]`、`[eslint]`）。

### 5.5 二进制探测

- 在**服务器将要拿到的** PATH 上 `exec.LookPath`，加上目录声明的 `extra_dirs`；macOS 先试 `xcrun --find <tool>`；rustup 的代理在组件未装时存在但会报错，因此以 `rust-analyzer --version` 的退出码为准；Windows 依赖 `PATHEXT`，能找到 npm 生成的 `.cmd` shim。
- 版本探测 3 秒超时，按「路径 + mtime」缓存到 `detect.json`。
- 探测一律在后台协程里做，不在 Load 或任何 UI 首帧路径上。

### 5.6 安装配方

- 配方 = 前置工具（`requires: go`）+ 命令 + 平台。
- **只在用户明确选择时执行**：推荐框的「安装并启用」、`/lsp` 面板的安装、`forebrain lsp install <id>`。界面**完整展示将执行的命令**，用户确认即授权；由 `process` 以用户身份在宿主执行（不经模型、不经模型的审批队列），输出写入日志、界面显示进度与结果尾部。
- 安装会写到工作区之外（`GOPATH/bin`、`~/.cargo`、npm 全局前缀）并需要网络，这正是它必须由用户显式发起的原因。
- 成功后重新探测，服务器按需启动。

---

## 6. `pkg/lsp` 运行时

### 6.1 传输与 JSON-RPC

- 只用 stdio。报文头 `Content-Length: N`（容忍 `Content-Type`），`\r\n\r\n` 后接 UTF-8 JSON。
- 单条消息上限 64 MiB（大工作区的 `workspace/symbol`、`references` 可能很大），超限按协议错误处理并重启实例。
- 请求 id 自增；`pending map[id]chan response`；每个请求带 ctx，ctx 取消时发 `$/cancelRequest` 并立即返回；迟到的响应丢弃。
- 服务器→客户端的请求在独立 goroutine 处理，**必须应答**，未知方法回 `-32601`。
- 写端单一队列，保证同一文档的 `didOpen / didChange / didSave` 顺序。
- stderr 写入实例日志（有界环形缓冲 + 文件），**永远不碰 TTY**（子进程写终端会弄花 TUI）。
- 协议类型用手写的 3.17 子集（约 1.5k 行）：LSP 的联合类型（`Location | Location[] | LocationLink[]`、`MarkupContent | MarkedString | MarkedString[]`、`DocumentSymbol[] | SymbolInformation[]`）需要自定义解码；第三方包要么停在 3.15/3.16，要么在 gopls 的 `internal` 里无法导入（§14 决策 6）。

### 6.2 初始化握手

- `initialize` 参数：`processId = os.Getpid()`（多数服务器在该进程消失时自行退出）、`clientInfo {name: forebrain, version}`、`rootUri` 与 `rootPath`（不少服务器仍依赖）、`workspaceFolders`、`initializationOptions`、`capabilities`（附录 A）、`trace: off`。
- **位置编码协商**：`general.positionEncodings: ["utf-8", "utf-16"]`；服务器回 `capabilities.positionEncoding`，缺省按 UTF-16 换算（§7.1）。
- `initialized` 之后，若 `settings` 非空，发一次 `workspace/didChangeConfiguration`；同时准备好应答 `workspace/configuration`（很多服务器只走拉取）。
- 保存 `ServerCapabilities`；每个工具操作先查能力，不支持时返回明确错误（如「pyright does not support type hierarchy」），不发请求。

### 6.3 服务器→客户端的请求与通知

| 方法 | 处理 |
|---|---|
| `workspace/configuration` | 按 section 路径从合并后的 `settings` 取值；Python 解释器自动探测回填 |
| `client/registerCapability` / `unregisterCapability` | 记录注册（重点：`workspace/didChangeWatchedFiles` 的 glob、动态注册的 `textDocument/diagnostic`） |
| `window/workDoneProgress/create` | 回 null，开始跟踪 token |
| `$/progress` | 更新索引进度（begin / report / end）→ 状态面板与就绪判断 |
| `window/showMessageRequest` | 按目录 `auto_answers` 选择；否则回 null（等同关闭对话框）；记日志 |
| `window/showMessage` / `window/logMessage` | 写日志；Error 级别进入状态面板的 `last_error` |
| `window/showDocument` | 回 `{success: false}` |
| `workspace/applyEdit` | 回 `{applied: false, failureReason: "read-only client"}`，记日志并计数 |
| `workspace/workspaceFolders` | 回当前实例的 folders |
| `workspace/diagnostic/refresh` | 标记需要重新 pull |
| `workspace/semanticTokens/refresh` 等 `*/refresh` | 回 null |
| `textDocument/publishDiagnostics` | 进诊断存储（带 version） |
| `experimental/serverStatus`（rust-analyzer） | `quiescent` / `health` → 就绪与错误 |
| `language/status`（jdtls） | `ServiceReady` → 就绪；`Error` → `last_error` |
| `telemetry/event` | 丢弃 |
| 其他请求 | `-32601 MethodNotFound` |

### 6.4 工作区根与实例粒度

- 根 = 从文件所在目录向上找最近的 `root_markers`，**不越过启动项目根**；找不到用项目根；项目根之外的文件不启动服务器（返回「outside the project」）。Rust 特例：取项目内最上层含 `[workspace]` 的 `Cargo.toml`。
- 实例键：`(服务器 id, 配置指纹, 根)`。服务器声明支持 `workspace.workspaceFolders.changeNotifications` 时，同一项目内的新根通过 `workspace/didChangeWorkspaceFolders` 加进已有实例（gopls、rust-analyzer、pyright、tsserver 都支持），monorepo 不会起一堆进程；否则起独立实例。
- 超过 `max_servers` 时，先关最久未用的空闲实例；没有空闲的就让请求失败，并提示「too many language servers running」及如何调大。

### 6.5 文档同步

- 每个实例一张打开表：`uri → {version, sha256, mtime, size, languageId, lastUsed}`。
- 任何针对文件的请求、任何写入之后，先 `ensureSynced(path)`：没打开就读盘 + `didOpen(version=1)`；打开了且 mtime/size 变化就读盘比哈希，变了就 `didChange`（全文替换，version+1；Full 与 Incremental 同步方式都接受无 range 的全文替换）。
- agent 写入后额外发 `didSave`（rust-analyzer 等在保存时跑检查；服务器要求 `includeText` 时带全文）。
- 每实例最多 64 个打开文档，LRU 关闭最久未编辑的。
- 超过 4 MiB、二进制、非 UTF-8 的文件不打开（结果里注明）。
- **什么时候开文档**：`lsp` 工具调用、agent 编辑会启动服务器并打开文档；`read_file` 只在服务器**已经在跑**时打开（便宜，用作诊断基线），**不会为了读文件启动服务器**。
- **首次编辑的基线**：编辑时若服务器还没跑，诊断全都会显得是「新的」。所以写工具把编辑前内容也交给 `DidWrite`（`edit_file` 有 `raw`、`write_file` 有 `before`、`apply_patch` 每个文件都有旧内容）：先 `didOpen(旧内容)` 取基线（等待上限为 `wait_ms` 的一半），再 `didChange(新内容)`。基线没取到（服务器还在索引）时，结果标注「baseline unavailable」并报告当前全部问题，而不是谎称都是新引入的。

### 6.6 工具之外的文件变化

- **已打开文档**：每次请求前比 mtime（§6.5），总是正确。
- **未打开文件**被 shell 改动（`git checkout`、代码生成、`go mod tidy`、`npm install`）：`shell` 工具完成后调用 `DidRunShell`，对运行中的实例做一次「变更扫描」——git 仓库里用 `git status --porcelain=v1 -z` 与上次快照比对，再加上各服务器关心的清单文件（`go.mod`、`Cargo.toml`、`package.json`、`pom.xml`、`build.gradle*`、`*.csproj`、`pyproject.toml` …）的 mtime；按服务器注册的 glob 发 `workspace/didChangeWatchedFiles`。节流：每秒最多一次、每次最多 2000 个文件。
- P3：用已在依赖里的 `fsnotify` 只监听清单文件与打开文档所在目录（不递归整个仓库）。

### 6.7 就绪与索引

- 状态：`starting → initializing → indexing(x%) → ready`，以及 `degraded / failed / stopped`。
- 就绪策略按目录的 `readiness`：`progress`（所有 work-done token 结束且安静 500 ms）、`rust-analyzer-status`（quiescent）、`jdtls-status`（ServiceReady）、`none`（`initialized` 即就绪）。
- 索引中的请求：在 `request_timeout` 内等就绪；超时仍然发请求，并在结果首行标注「language server is still indexing; results may be incomplete」——模型必须知道结果可能不全。

### 6.8 生命周期

- **懒启动**的触发：`lsp` 工具作用于匹配文件、agent 编辑匹配文件、`/lsp restart`、`prewarm: true`（会话载入后在后台启动，从不阻塞 Load）。与 MCP 不同，**Load 只登记工具，从不启动服务器**。
- **崩溃**：supervisor 发现退出 → `failed`；允许重启且 10 分钟内次数未超 `max_restarts` 时按 1 s、2 s、4 s 退避重启，重新 initialize，按打开表从磁盘重新打开文档；进行中的请求返回「server restarted」。
- **优雅关闭**：`shutdown` 请求（`shutdown_timeout`）→ `exit` 通知 → 等 1 s → 杀进程树。
- **进程树清理**：Unix 用 `Setpgid` + `kill(-pgid)`（先 SIGTERM，2 s 后 SIGKILL）；Linux 额外设 `Pdeathsig: SIGKILL`，forebrain 被杀时子进程随之退出；macOS 没有 pdeathsig，用 `state/lsp/pids.json` 记录，下次启动时核对进程启动时间后清扫残留；Windows 用 Job Object + `JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE`。
- **空闲回收**：实例超过 `idle_timeout` 无请求、无编辑即关闭；gateway 的 `RunnerPool` 在释放空闲 MCP 连接的同一时刻（`runner_pool.go:293`）调用 `ReleaseIdle`。
- **引用计数**：Runner 持有池的引用；Runner `Close`、`Environment` 关闭时释放；引用归零的实例关闭。

### 6.9 日志与可观测性

- 每实例一个轮转日志文件；`/lsp` 面板能看路径和尾部。
- 遥测 span（沿用 `pkg/telemetry`）：`lsp.start`、`lsp.request{method, duration, result_count}`、`lsp.diagnostics.wait`。
- 状态快照订阅（照 `mcpStatusHub` 的做法）供 TUI 与 Web 使用。

### 6.10 资源预算

- 默认 `max_servers: 6`（TUI）、gateway 全进程 12。
- JVM 系（jdtls、kotlin-lsp、metals）默认带保守的堆上限参数（如 jdtls 的 `--jvm-arg=-Xmx2G`），doctor 显示实例常驻内存。

---

## 7. 面向模型的能力

### 7.1 `lsp` 工具

```go
type LSPInput struct {
    Operation string `json:"operation" jsonschema:"enum=definition,enum=declaration,enum=type_definition,enum=implementation,enum=references,enum=hover,enum=document_symbols,enum=workspace_symbols,enum=incoming_calls,enum=outgoing_calls,enum=supertypes,enum=subtypes,enum=diagnostics"`
    FilePath           string `json:"file_path,omitempty"`
    Line               int    `json:"line,omitempty"`   // 1-based，与 read_file 显示的行号一致
    Column             int    `json:"column,omitempty"` // 1-based，按字符计
    Symbol             string `json:"symbol,omitempty"` // 用该行上的名字定位，代替 column
    Query              string `json:"query,omitempty"`  // workspace_symbols
    IncludeDeclaration bool   `json:"include_declaration,omitempty"`
    MaxResults         int    `json:"max_results,omitempty"` // 默认 50，上限 200
}
```

| operation | LSP 方法 |
|---|---|
| `definition` / `declaration` / `type_definition` / `implementation` | `textDocument/definition` / `declaration` / `typeDefinition` / `implementation` |
| `references` | `textDocument/references` |
| `hover` | `textDocument/hover` |
| `document_symbols` | `textDocument/documentSymbol` |
| `workspace_symbols` | `workspace/symbol`（必要时 `workspaceSymbol/resolve`） |
| `incoming_calls` / `outgoing_calls` | `textDocument/prepareCallHierarchy` + `callHierarchy/incomingCalls` / `outgoingCalls` |
| `supertypes` / `subtypes` | `textDocument/prepareTypeHierarchy` + `typeHierarchy/supertypes` / `subtypes` |
| `diagnostics` | 有 `file_path`：`textDocument/diagnostic`（pull）或推送缓存；无 `file_path`：`workspace/diagnostic`（支持时）或已打开文档的汇总 |

**为什么要 `symbol`**：`read_file` 只显示行号（`N|text`，见 `sliceWithLineNumbersFromReader`），模型数列号不可靠，碰到 tab 和多字节字符更容易错。按名字锚定：在该行按标识符边界找第一次出现（`Foo.bar` 取最后一段）；该行没有就在 ±3 行内找，并在结果里写明实际使用的位置；`column` 超出行长时截到行尾并注明。之后换算成服务器协商的编码（UTF-8 / UTF-16 / UTF-32）。

**模型可见的描述**（编译期常量、英文、provider-neutral、不列语言、不提其他工具名）：

> Look up code through the project's language server: definitions, references, implementations, type information, symbols, call and type hierarchies, and current diagnostics. Read-only. Lines are 1-based, numbered the way file reads show them; pass `symbol` (a name on that line) instead of `column` when unsure of the column. Prefer this over text search to find where a symbol is defined or used.

`ToolMeta{ReadOnly: true, ConcurrencySafe: true, Category: "code", InterruptBehavior: "cancel"}`：调度器可以并行跑多个查询（JSON-RPC 本来就多路复用，文档同步按文档串行）。

### 7.2 输出格式

路径相对项目根，按文件分组，行列 1-based；每个位置带一行源码预览。以下示例的路径仅作示意：

```
references to `ApplyDiscount`: 4 in 3 files
internal/billing/discount.go
  41:6    func ApplyDiscount(inv *Invoice, pct float64) error {   [definition]
internal/billing/invoice.go
  88:12   if err := ApplyDiscount(inv, rate); err != nil {
  131:9   return ApplyDiscount(inv, 0)
cmd/billing/main.go
  57:15   err = billing.ApplyDiscount(inv, *flagPct)
```

- `hover`：签名代码块 + 文档，截断到 2000 字符。
- `document_symbols`：缩进大纲，带行范围（`func (r *Runner) Close  2960-2990`）。
- 可读根之外的位置（标准库、模块缓存、项目外的 `node_modules`）：只给绝对路径 + 行列，标 `[outside project]`，**不给预览**；模型要看内容就走 `read_file` 的正常权限流程。
- 超过 `max_results`：先报总数再截断；大结果按现有 spill 机制落盘（`tool.SpillDir`），用 `retrieve_output` 取回。
- 给界面的 `CaptureToolOutput`：`operation`、`result_count`、`files`、`server`、`elapsed_ms`、`indexing`。

### 7.3 编辑后诊断

流程：

1. `write_file` / `edit_file` / `apply_patch` 成功后，若 `rt.CodeIntel != nil` 且 `diagnostics.after_edit`，调用 `DidWrite(ctx, []FileChange{{Path, Before, After}})`。没有服务器负责的文件零开销。
2. 管理器确保实例存在（必要时懒启动）；启动超过等待窗口就先返回「language server starting; diagnostics will follow」，诊断转入迟到队列（§7.4）。
3. 取基线（§6.5），发 `didChange` + `didSave`。
4. 取诊断：服务器支持 pull 时直接 `textDocument/diagnostic`（确定性最好）；否则等待该 uri 的推送（有 `versionSupport` 时要求 version ≥ 刚发送的 version），之后再安静 300 ms 收集其他文件的推送；总时长不超过 `wait_ms`。
5. **只报新引入的**：`新 = 多重集(之后) − 多重集(基线)`，键为 `(severity, source, code, message)`——不用行号做键，编辑造成的行号偏移不会让老问题被误报为新问题。
6. **跨文件**：改函数签名会让调用方报错。窗口内推送了变化的其他项目内文件也纳入「新问题」。
7. 过滤 `min_severity`，按 `max_per_file` / `max_files` 截断，错误在前。
8. 追加到工具结果（成功文本之后）：

```
ok

<diagnostics>
2 new problems after this edit (gopls)
internal/billing/invoice.go
  error 57:14 undefined: taxRate [compiler UndeclaredName]
internal/billing/invoice_test.go
  error 12:3 not enough arguments in call to NewInvoice [compiler WrongArgCount]
</diagnostics>
```

   没有新问题时**不追加任何文字**（不浪费 token，与 Claude Code 一致）。
9. `CaptureToolOutput` 加 `diagnostics: {new, files, items}`，界面据此画「Found N new diagnostic issues in M files」。
10. 诊断在写入**成功之后**收集，不影响审批与沙箱；计划模式只能写计划文件（Markdown），没有服务器，天然空操作。

### 7.4 迟到诊断

- rust-analyzer（保存时 `cargo check`）、jdtls（增量编译）、metals（Bloop 编译）的诊断常在等待窗口之后才到。
- 管理器按 agent 会话记录「本会话编辑过的文件」（最近 30 分钟）。这些文件收到新推送、且与上次报告的不同，就把增量放进该会话的待办队列。
- 新的 LLM wrapper `wrapLSPDiagnosticsReminderLLM`，挂在 `wrapSkillOfferLLM` 旁边、同一个 `reminderAdoptionSink`：每次模型请求前按 `AgentSessionID` 取出待办，在末尾插入提醒。提醒的外框文字是常量（「Language server diagnostics changed for files edited earlier in this session:」），内容附在后面；经 `recordReminderAdoption` 进入 transcript，不会重复注入。
- 没有待办时不注入；按 `(文件, 诊断摘要)` 去重。
- 这个 wrapper 在压缩用的 `summaryChain` **之外**（与 plan-mode 提醒同理），摘要请求不会带上它。

### 7.5 工具注册与 prompt 缓存

- 在 `loadLocked` 里紧跟 `tool.RegisterDefaultTools`（`pkg/run/runner.go:1176`）调用 `registerCodeIntelTool`，条件来自冻结的 `Deps.CodeIntelTool`（§4.3）。
- 描述与 schema 是常量，所有会话、所有项目字节相同；加一个 golden 测试钉住 schema JSON。
- **不改系统提示**：使用指引写在工具描述里，没有该工具的会话完全不受影响（不照搬 `wrapCodegraphPromptLLM` 那种按工具存在与否改系统消息的做法）。
- 启停、崩溃重启、推荐接受、配置热重载都不改工具表；新增测试断言这些操作前后 `LoadedTools()` 字节一致。

### 7.6 可见性

- 计划模式：允许（只读）。
- 类型化子代理：`VisibleToolsForSubagentSubtype`（`pkg/tool/registry.go:408`）是黑名单，不加入即对 explore / plan / verification / general-purpose 全部可见——它们正是最需要导航能力的。
- 子代理与 fork：`Factory.NewIsolatedRunner`（`pkg/run/factory.go:46`）必须新增 `CodeIntel` 与 `CodeIntelTool` 的继承（`ownerCodeIntel()`），否则子会话没有 LSP，fork 的工具表还会与父会话不一致。
- guardian 审批员：不用工具，不受影响。

---

## 8. 权限与安全

### 8.1 威胁模型：打开项目 ≈ 构建项目

| 服务器 | 会执行的项目内容 |
|---|---|
| rust-analyzer | `build.rs`、过程宏（打开项目即运行） |
| jdtls / kotlin-lsp / metals | Maven / Gradle / sbt 构建脚本与插件，且会联网下载依赖 |
| gopls | `go list`；`GOTOOLCHAIN=auto` 时按 `go.mod` 的 `toolchain` 下载并运行工具链；cgo 调 `pkg-config` |
| typescript-language-server | tsconfig 的 `compilerOptions.plugins` 加载 `node_modules` 里的代码 |
| pyright / intelephense / clangd | 基本只解析，风险较低 |
| 项目级配置 | `rust-analyzer.check.overrideCommand`、`cargo.buildScripts.overrideCommand`、pyright `python.pythonPath`、gopls `build.env` 等可让服务器运行任意程序 |

结论：风险级别与 MCP stdio 服务器相同，闸门也应相同。

### 8.2 启动闸门（全部满足才启动）

1. `features.lsp` 为真。
2. 会话有启动项目且**受信任**（`safety.TrustedRoot`）。未受信任：不启动、不推荐、不注册工具。
3. 服务器**已启用**：用户全局显式启用，或接受过推荐。目录条目默认不启用。
4. 项目级条目或覆盖：逐条同意，指纹变化重新询问。
5. （P3）`sandbox: required` 而后端不可用：不启动（fail closed）。

### 8.3 环境变量

基础白名单（`envguard.go`）+ 目录的 `env_passthrough` + 显式 `env`；像密钥的名字仍被过滤。项目条目：不能扩展 `env_passthrough`，`${VAR}` 只能取自 `~/.forebrain/.env`（与 MCP 的 `envLookupForServer` 相同）。

### 8.4 工具权限

- `canonicalToolAliases` 加 `"lsp": "LSP"`；`LSP` 可以作为规则名整体 allow / deny。
- **`Read(...)` 路径规则同样约束 `LSP`**（Claude Code 的语义）：`Read(~/secrets/**)` 的 deny 同样挡住对这些路径的 LSP 调用。实现上在规则匹配处把 `LSP` 视为 Read 类。
- 工具在联系服务器之前，对 `file_path` 做与 `read_file` 完全相同的授权：`resolveReadFilePath`、`ProtectedReadReason`（走审批）、`ReadPathDenied`（`file_tools.go:60-105`）。把这段抽成 `authorizeRead(ctx, st, path)` 两边共用，避免两份逻辑漂移。
- 通过授权的调用默认放行（加入 `safeReadOnlyTools`，理由写在注释里：只读、路径已按 read 规则授权）。
- `workspace_symbols` 没有文件参数：放行，但结果里只有可读根内的位置带预览（§7.2）。

### 8.5 只读保证

- `ClientCapabilities.workspace.applyEdit = false`，且 `workspace/applyEdit` 请求一律拒绝。
- 从不发送 `workspace/executeCommand`；P0–P2 不做 formatting、rename、code action。
- P3 的 `rename_preview`：`textDocument/rename` 得到 WorkspaceEdit → 转成统一 diff 返回给模型；真正落盘仍由模型调用 `apply_patch` / `edit_file`，走现有审批。

### 8.6 沙箱（分期）

- **P0–P2**：与 MCP stdio 服务器同级，在宿主上、OS 沙箱之外运行（先例：`bridge.go:501`），由 §8.2 的闸门把关；`/lsp` 与文档明确写出这一点。
- **P3**：新增 `safety.Manager.PrepareLongRunning(ctx, cfg, LongRunningRequest) (*exec.Cmd, error)`，用 bubblewrap（Linux）/ seatbelt（macOS）包装长驻进程。现有 `Manager.RunCommand`（`pkg/safety/manager.go:294`）是一次性、带超时、捕获输出的，不能直接用。LSP 配置档：
  - 读：项目根 + 工具链目录（解析出的二进制所在目录、`GOROOT`、`JAVA_HOME`、SDK 目录）+ 各自缓存目录；
  - 写：LSP 缓存目录 + 已知构建输出目录（`target/`、`build/`、`.gradle/`、`.metals/`、`.bloop/`、`.bsp/`、`.cache/clangd/`、`.build/`、`obj/`、`bin/`）+ 临时目录；
  - 网络：按服务器（jdtls / metals / kotlin-lsp 默认开，pyright / clangd / tsserver 默认关）。
  - Windows 没有长驻后端：`auto` 回退宿主运行并在 `/lsp` 标黄，`required` 拒绝启动。

---

## 9. 推荐（截图对应的功能）

### 9.1 触发条件（全部满足）

- 交互式界面：TUI 或 Web 会话。通道适配器、非交互运行、定时任务不推荐。
- `features.lsp && lsp.recommendations`，且 `recommendations.json` 没有 `disabled`。
- 启动项目受信任。
- **本会话中 agent 编辑了**扩展名命中目录服务器的文件（与 Claude Code 一致，§14 决策 2）。
- 该服务器未启用、不在 `never` 列表；**本会话还没推荐过任何服务器**（每会话最多一次）。
- 二进制已找到 → 推荐「启用」；没找到但安装配方的前置工具在（`go`、`rustup`、`npm`、`dotnet`、`cs`、`brew`…）→ 推荐「安装并启用」；两者都没有 → 不弹框，只在 `/lsp` 里显示安装指引。

### 9.2 选项与持久化

| 选项 | 效果 | 落盘 |
|---|---|---|
| **1. Yes, enable** | 写入启用覆盖；**立即在后台启动服务器**；下一次编辑起就有诊断；本会话若没注册 `lsp` 工具，界面提示「新会话（/new）起可用代码导航」 | `enabled.json` |
| **1'. Yes, install and enable** | 展示完整安装命令 → 执行（§5.6）→ 成功后同上；失败显示输出尾部 | 同上 |
| **2. No, not now**（Esc 同） | 本会话不再推荐；以后的会话可以再推荐 | — |
| **3. Never for `<server>`** | `never` 加入该 id | `recommendations.json` |
| **4. Disable all LSP recommendations** | `disabled = true` | `recommendations.json` |
| 30 秒无操作 | 关闭，`ignored_count + 1`；累计 5 次等同选项 4 | `recommendations.json` |

重新开启：`/lsp` 面板的「Reset recommendations」，或 `forebrain lsp recommendations reset`（清 `disabled`、`ignored_count`、`never`）。

### 9.3 事件流

```
write/edit 工具 ──DidWrite──▶ lsp.recommend 判定
                                   │ 回调（process 装配时注入，携带 agent session）
                                   ▼
                     Runner.publishSurfaceEvent(RunEvent{type: "lsp_recommendation"})
                                   │
                ┌──────────────────┴──────────────────┐
                ▼                                     ▼
      TUI：非模态浮层（数字键 1–4 / Esc）     Web：聊天流中的推荐卡片
                │                                     │ POST /api/v1/lsp/recommendations/:id/decision
                └──────────▶ CodeIntelControl.DecideRecommendation ◀──┘
```

- **非阻塞**：与审批不同，推荐不暂停 agent；回合继续，接受后从下一次编辑起生效。
- 决策结果在界面 transcript 里留一行提示（如「gopls enabled for Go」），**不进入模型上下文**。

### 9.4 TUI 样式（对齐截图）

已安装：

```
LSP recommendation

  A language server gives the agent diagnostics after its edits and
  lets it find definitions and references by symbol.

  Server:       gopls (Go)
  Found:        ~/go/bin/gopls (v0.20.0)
  Triggered by: .go files
  Runs:         in this trusted project, outside the sandbox

  Enable this language server?
  ❯ 1. Yes, enable
    2. No, not now
    3. Never for gopls
    4. Disable all LSP recommendations
```

未安装：`Found:` 一行换成 `Not installed. Install with: go install golang.org/x/tools/gopls@latest`，选项 1 变成 `Yes, install and enable`。Web 卡片同样内容，走 i18n（en / zh）。

---

## 10. 界面、命令与迁移

### 10.1 `/lsp`

在 `pkg/turn/slash.go:73` 的命令表里登记：`{Name: "lsp", Description: "language servers: status, enable, restart, diagnostics", AllowedSurfaces: {WebChat, TUI}}`，处理方式与 `/mcp` 相同（面板型、回合进行中也可用）。

```
Language servers — ~/src/shop (trusted)
  ● gopls                       Go        ready          root .        3 open · 0 errors
  ◐ rust-analyzer               Rust      indexing 42%   root crates/
  ○ pyright                     Python    enabled · not started
  ✕ jdtls                       Java      failed: needs a newer JDK (found 17)
  – clangd                      C/C++     available (/usr/bin/clangd) · not enabled
  – typescript-language-server  TS/JS     not installed · npm install -g typescript-language-server typescript
```

操作：Enter 看详情（根、pid、能力、`last_error`、日志路径与尾部）；`e` 启用/停用；`r` 重启；`i` 安装；`d` 当前诊断；`R` 重新开启推荐。

### 10.2 编辑卡片的诊断行

`edit_file` / `write_file` / `apply_patch` 卡片下多一行 `Found 2 new diagnostic issues in 1 file`，明细用 TUI 现有的折叠块（`foldBlock` 的点击展开）；Web 的 `ToolCallCard.vue` 用同样的折叠区。

### 10.3 `lsp` 工具卡片

标题按操作用动词：「Found definition · ApplyDiscount」「Found 12 references in 5 files」「Listed symbols · invoice.go」；失败文案走 `failedActionPhrase`（`look up`）；回放时由 `replayToolDisplayBody` 重建。

### 10.4 `/status`

加一行：`Language servers: 2 running, 1 indexing, 1 failed`。

### 10.5 Web

- REST（`pkg/gateway/api_extra.go` 新增 `/v1/lsp` 组）：`GET /servers`、`POST /servers/:id/enable|disable|restart|install`、`GET /servers/:id/log`、`POST /recommendations/:id/decision`、`POST /recommendations/reset`；项目：`GET /v1/projects/:id/lsp`、`POST /v1/projects/:id/lsp/consent`。
- WS：`lsp_recommendation`、`lsp_status` 事件。
- 前端：`views/LspView.vue`（照 `McpView.vue`）、`components/chat/LspRecommendationCard.vue`、`ToolCallCard.vue` 的诊断折叠区、路由与导航项、`locales` 的 en / zh 文案、对应的 vitest（照 `McpView.test.ts`）。

### 10.6 命令行

`cmd/forebrain` 新增 `forebrain lsp` 子命令（`cmd` 不在分层表里，可以直接导入 `pkg/lsp`）：

- `forebrain lsp list`：目录 + 探测结果 + 启用状态。
- `forebrain lsp doctor [--lang go] [--project .]`：对相关服务器检查二进制、版本、根标记、传给它的环境变量，做一次真实 `initialize` + 对样例文件的 `documentSymbol`，报耗时并给出修复建议（缺 `compile_commands.json`、JDK 版本不够、没 `dotnet restore` 等）。
- `forebrain lsp enable|disable <id>`、`forebrain lsp install <id>`、`forebrain lsp recommendations reset`。

### 10.7 `/migrate`

扩展 Claude 源：沿用 `discoverPluginSkills`（`pkg/migrate/claude.go:355`）的插件发现（`settings.json` 的 `enabledPlugins` → `plugins/cache/<marketplace>/<name>/<最高版本>/`），读 `.lsp.json` 与 `plugin.json` 的 `lspServers`（路径或内联），按 §4.5 映射；命令命中目录的只启用目录 id。同时把 `~/.claude.json` 的 `lspRecommendationDisabled`、`lspRecommendationNeverPlugins`（插件名映射到目录 id）搬进 `recommendations.json`。幂等；dry-run 报告每一条的去向。Codex 源没有 LSP，不涉及。`migrate` 只写 `config` 结构，fan-out 不变。

---

## 11. 分期实施

每期一个或多个 PR，标题 `feat(lsp): …`；每个 PR 独立可合并、默认行为安全。

### P0 运行时底座（用户无感：没有任何服务器默认启用）

| 任务 | 内容 | 主要文件 |
|---|---|---|
| T0.1 | `lsp` 配置段、`features.lsp`、校验、序列化往返测试 | `pkg/config/lsp.go`、`config.go` |
| T0.2 | JSON-RPC、协议子集、位置换算、URI 规范化（含 fuzz） | `pkg/lsp/jsonrpc/`、`protocol/`、`position.go`、`uri.go` |
| T0.3 | client + supervisor + 进程树清理；假服务器测试 | `client.go`、`supervisor.go`、`procgroup_*.go` |
| T0.4 | 文档同步与诊断存储 | `docsync.go`、`diagnostics.go` |
| T0.5 | 12 门必选语言目录、探测、根解析 | `catalog/`、`detect.go`、`root.go` |
| T0.6 | 池与管理器；port 接口；DTO | `pool.go`、`manager.go`、`pkg/tool/code_intel.go`、`pkg/event/lsp.go` |
| T0.7 | 组合根接线：`Environment` 持有池、`Deps.CodeIntel`/`CodeIntelTool`、`Factory` 继承、`RunnerPool` 空闲释放、关闭 | `pkg/process/open.go`、`runner_pool.go`、`pkg/run/runner.go`、`factory.go` |
| T0.8 | 分层表、`graph.json` | `pkg/architecture/cache_test.go`、`testdata/graph.json` |
| T0.9 | `forebrain lsp list / doctor` | `cmd/forebrain/lsp.go` |

验收：CI 里 `forebrain lsp doctor` 对 gopls、pyright、typescript-language-server、rust-analyzer、clangd 的 fixture 项目全部通过；进程泄漏测试通过。

### P1 模型能力

| 任务 | 内容 |
|---|---|
| T1.1 | `lsp` 工具 + `authorizeRead` 抽取 + safety 别名与 Read 规则 |
| T1.2 | `write_file` / `edit_file` / `apply_patch` 的编辑后诊断；读观察者 fan-out；shell 后变更扫描 |
| T1.3 | 迟到诊断提醒 wrapper |
| T1.4 | 注册冻结 + 缓存稳定性测试 + schema golden |
| T1.5 | TUI / Web 的最小渲染：`lsp` 卡片、诊断行 |

验收：用 run-forebrain skill 的假 provider 驱动一次「编辑引入错误 → 工具结果带诊断 → 修复」，TUI 与 Web 都看得到诊断行。

### P2 产品化

| 任务 | 内容 |
|---|---|
| T2.1 | 推荐引擎 + 状态文件 + 事件；TUI 浮层；Web 卡片 |
| T2.2 | `/lsp` 面板（TUI + Web）+ REST + `/status` 行 |
| T2.3 | 项目级 `lsp_servers.yaml` + 同意（终端启动提示 + Web 项目页） |
| T2.4 | 安装配方执行 |
| T2.5 | `/migrate` 导入 |
| T2.6 | 扩展语言目录；诊断型服务器（ruff、ESLint、Biome） |
| T2.7 | 文档站 `config/lsp.md`（en + zh 镜像）、README 功能表、FOREBRAIN.md 架构段 |

### P3 进阶

| 任务 | 内容 |
|---|---|
| T3.1 | 长驻进程沙箱 API + LSP 配置档 |
| T3.2 | `rename_preview` / `code_actions` 预览操作 |
| T3.3 | `fsnotify` 清单文件监听；全项目诊断操作 |
| T3.4 | C# Roslyn LS 等备选；Swift 的 Xcode 工程（xcode-build-server）引导 |
| T3.5 | gateway 多项目调优（全进程上限、按项目空闲策略） |

---

## 12. 测试计划

- **单元**：JSON-RPC 分帧（头部被拆开、一次读到多条、超大消息）、取消；位置换算（UTF-16 代理对 😀、CJK、tab、CRLF、BOM）；`symbol` 锚定；URI 规范化（Windows 盘符 `%3A`、macOS/Windows 大小写不敏感、符号链接两侧 `EvalSymlinks`）；诊断多重集增量；根解析（monorepo fixture）；目录校验；配置合并与同意指纹；环境变量构建。
- **假服务器**（`pkg/lsp/internal/fakeserver`，用 `TestMain` 自执行的方式作为子进程启动）：可编排能力声明、慢索引、第 N 个请求后崩溃、服务器→客户端请求、push 与 pull 诊断、`applyEdit` 尝试、stdout 杂讯。
- **进程泄漏**：服务器再起孙进程；`Close` 后断言整棵树消失（Unix 进程组、Windows Job Object）；`kill -9` forebrain 后 Linux 由 pdeathsig 回收、macOS 下次启动清扫。
- **集成**（build tag `lsp_integration`，新 CI job，先 non-blocking，稳定后把 Go / Python / TS 设为 required）：`pkg/lsp/testdata/projects/{go,python,typescript,rust,c,cpp}`；nightly job 覆盖 java / kotlin / scala / swift（macOS runner）/ csharp / php。
- **工具层**：输出 golden；权限（Read deny 规则挡住 LSP；根外路径走审批）；计划模式可用；子代理可见；fork 工具表与父一致。
- **缓存**：`lsp` schema 字节 golden；会话中启用服务器前后 `LoadedTools()` 字节一致；相同冻结输入的重载产生相同工具表。
- **TUI**：reducer / render 测试覆盖推荐浮层与诊断行；用 run-forebrain skill 实机驱动 1–4、Esc、30 秒超时。
- **前端**：`LspView` 与推荐卡片的 vitest。
- **架构**：分层测试更新；`scripts/package-graph.sh` 无差异。
- **平台**：`GOOS=windows go vet ./...`；Windows 套件覆盖 `.cmd` shim 启动（Go 对 `.bat/.cmd` 参数的转义路径）与 Job Object。

---

## 13. 风险与对策

| 风险 | 影响 | 对策 |
|---|---|---|
| 服务器吃内存 / CPU（jdtls、rust-analyzer、kotlin-lsp） | 笔记本卡顿 | 懒启动、`max_servers`、`idle_timeout`、JVM 堆上限默认参数、doctor 显示内存 |
| 首次索引慢 | 第一次编辑拿不到诊断 | 迟到诊断提醒；结果标注「indexing」；`prewarm` 可选 |
| 每次编辑多等 | 会话变慢 | `wait_ms` 默认 2.5 s、优先 pull、无服务器的文件零开销、可关 |
| 服务器往项目里写东西（`.cache/clangd`、`.metals`、`.bloop`、`.bsp`、`target/rust-analyzer`、`.build`） | git 状态被弄脏 | 目录默认设置把能重定向的都重定向（jdtls metadata、rust-analyzer targetDir）；doctor 提示补 `.gitignore`；`project_writes` 写进文档 |
| 执行项目代码 | 安全 | 受信任 + 显式启用 + 逐条同意；P3 沙箱 |
| 诊断误报（monorepo、没有编译数据库） | 误导模型 | 只报新引入的；doctor 提示；每个服务器可关诊断 |
| 与 agent 自己的构建争锁（cargo target、Gradle daemon） | 构建变慢或等锁 | rust-analyzer 独立 targetDir；jdtls 独立 `-data`；文档说明 |
| 服务器行为不标准 | 结果不一致 | 能力驱动、目录 quirks、假服务器矩阵、集成测试 |
| 模型滥用 `workspace_symbols` | token 消耗 | 结果上限、截断、spill |
| 孤儿进程 | 资源泄漏 | 进程组 / Job Object / pdeathsig / pidfile 清扫；`processId` 让服务器自行退出 |
| Windows 路径与 URI | 定位失败 | URI 规范化 + Windows CI |

---

## 14. 需要拍板的决策（带推荐）

| # | 问题 | 推荐 | 备选与代价 |
|---|---|---|---|
| 1 | 目录服务器默认启用吗？ | **否**，用户显式启用一次（推荐框一键） | 检测到二进制即自动启用：更省事，但在受信任项目里会自动执行构建脚本（§8.1） |
| 2 | 推荐何时触发？ | **编辑时**（与 Claude Code 一致） | 读取时也触发：更早拿到诊断，但「只是看看代码」也会被打扰 |
| 3 | `lsp` 工具何时注册？ | **受信任项目且冻结时有已启用服务器** | `features.lsp` 开就注册：中途启用立刻可用，但每个会话都多一段固定前缀，且在无服务器的项目里模型可能徒劳调用 |
| 4 | 允许 forebrain 执行安装命令吗？ | **允许**，但必须完整展示命令并由用户确认；终端与 Web 都可 | 只显示命令让用户自己执行：更保守，但「安装并启用」一步到位的体验没了 |
| 5 | P0–P2 在沙箱外运行吗？ | **是**，与 MCP stdio 同级，由 §8.2 闸门把关；P3 提供沙箱 | 一开始就做沙箱：安全更好，但工期显著变长，且 jdtls / metals 需要的网络与缓存路径要先摸清 |
| 6 | 协议类型自己写还是用第三方？ | **自己写 3.17 子集** | 第三方包：省代码，但版本老、联合类型处理不全，且多一个大依赖 |
| 7 | 诊断附在工具结果还是只走提醒？ | **同步窗口内附在工具结果，迟到的走提醒** | 只走提醒：实现更简单，但模型要到下一次请求才看到，且提醒与结果分离不利于对应 |

---

## 附录 A：`initialize` 的 ClientCapabilities（草案）

```json
{
  "general": { "positionEncodings": ["utf-8", "utf-16"] },
  "workspace": {
    "applyEdit": false,
    "workspaceFolders": true,
    "configuration": true,
    "didChangeConfiguration": { "dynamicRegistration": true },
    "didChangeWatchedFiles": { "dynamicRegistration": true, "relativePatternSupport": true },
    "symbol": { "dynamicRegistration": false, "resolveSupport": { "properties": ["location.range"] } },
    "diagnostics": { "refreshSupport": true },
    "workspaceEdit": { "documentChanges": true }
  },
  "textDocument": {
    "synchronization": { "didSave": true, "willSave": false, "dynamicRegistration": false },
    "publishDiagnostics": { "relatedInformation": true, "versionSupport": true, "codeDescriptionSupport": true, "dataSupport": false, "tagSupport": { "valueSet": [1, 2] } },
    "diagnostic": { "dynamicRegistration": true, "relatedDocumentSupport": true },
    "hover": { "contentFormat": ["markdown", "plaintext"] },
    "definition": { "linkSupport": true },
    "declaration": { "linkSupport": true },
    "typeDefinition": { "linkSupport": true },
    "implementation": { "linkSupport": true },
    "references": {},
    "documentSymbol": { "hierarchicalDocumentSymbolSupport": true },
    "callHierarchy": {},
    "typeHierarchy": {}
  },
  "window": { "workDoneProgress": true, "showMessage": { "messageActionItem": { "additionalPropertiesSupport": false } }, "showDocument": { "support": false } }
}
```

`codeAction`、`rename`、`formatting`、`completion`、`semanticTokens` 刻意不声明（§1.3、§8.5）。

## 附录 B：完整配置示例

```yaml
# ~/.forebrain/forebrain.yaml
features:
  lsp: true
lsp:
  recommendations: true
  max_servers: 6
  idle_timeout: 600
  diagnostics:
    after_edit: true
    wait_ms: 2500
    min_severity: warning
  servers:
    rust-analyzer:
      settings:
        rust-analyzer:
          cargo: { targetDir: true, features: all }
    jdtls:
      args: ["--jvm-arg=-Xmx3G"]
    ruff:
      enabled: true          # 诊断型，与 pyright 并行
```

```yaml
# <project>/.forebrain/lsp_servers.yaml —— 受信任 + 受版本控制 + 逐条同意
servers:
  gopls:
    settings:
      gopls:
        buildFlags: ["-tags=integration"]
  typescript-language-server:
    initialization_options:
      tsserver: { path: "node_modules/typescript/lib" }
```

## 附录 C：文件变更清单

| 包 / 目录 | 新增或改动 |
|---|---|
| `pkg/lsp/`（新） | §3.3 全部文件；`doc.go` |
| `pkg/config` | `lsp.go`（配置段）；`config.go`（`Root.LSP`、`FeaturesSection.LSP`、`EffectiveFeatures`） |
| `pkg/event` | `lsp.go`（DTO）；`run_events.go`（`lsp_recommendation`、`lsp_status`） |
| `pkg/tool` | `code_intel.go`（port）；`lsp_tool.go`；`file_tools.go`（写后诊断、`authorizeRead`）；`shell_tool.go`（`DidRunShell`）；`state.go`（读观察者 fan-out）；`registry.go`（`AgentToolRuntime.CodeIntel`） |
| `pkg/safety` | `permission_rules.go`（别名）；`engine.go`（Read 类匹配、`safeReadOnlyTools`）；P3：长驻沙箱 |
| `pkg/run` | `runner.go`（`Deps.CodeIntel`/`CodeIntelTool`、注册、Close）；`factory.go`（继承）；新 wrapper（迟到诊断） |
| `pkg/turn` | `slash.go`（`/lsp`） |
| `pkg/process` | `open.go`（池、冻结、注入）；`runner_pool.go`（空闲释放）；`lsp.go`（生效服务器解析、同意、推荐回调、安装执行） |
| `pkg/tui` | `/lsp` 面板、推荐浮层、诊断行、`lsp` 卡片、`/status` 行、启动时项目条目同意 |
| `pkg/gateway` | `/v1/lsp` 路由与 handler、项目 LSP 同意、WS 事件 |
| `frontend` | `LspView.vue`、`LspRecommendationCard.vue`、`ToolCallCard.vue`、路由、导航、`locales` |
| `pkg/migrate` | Claude 源的 LSP 插件与推荐状态导入 |
| `cmd/forebrain` | `lsp.go`（`list / doctor / enable / disable / install / recommendations reset`）；`mcp_consent.go` 旁加项目 LSP 同意 |
| `pkg/architecture` | 分层表、同层边、fan-out 预算、`testdata/graph.json` |
| 其他 | `.gitmessage` 的 scope 加 `lsp`；文档站 `config/lsp.md`（en + zh）；README 功能表；FOREBRAIN.md 架构段 |
