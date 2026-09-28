# /status 与 /mcp 修复与重设计方案

状态：已批准（2026-09-23），实施前已做深度评审修订（见 §11）
范围：TUI（语义基准）+ Gateway/Web（共享引擎复用，保持对等）

本期适用的 owner 规则（每条都要能在验收中逐项核对）：

- **R-cache**：prompt 缓存命中率只能上升，不能下降，优先级高于一切。
- **R-model**：斜杠命令的输出只给用户看，任何命令、任何 surface 都不进入模型 transcript。
- **R-json**：所有斜杠命令的浮层和输出都禁止显示原始 JSON，必须是语义化、可读的界面。
- **R-args**：内联参数尽可能少，只保留必需的；凡是有内联参数的命令，都必须在输入时显示参数提示。
- **R-full**：长内容用滚动完整呈现，禁止截断、折叠或省略号。
- **R-fix**：修根因不修症状；同类组件一起修；死代码彻底删除。

---

## 1. 根因

### 1.1 现象（真实 TUI 复现）

用 `run-forebrain` 驱动真实 TUI，依次输入 `/status`、`/mcp`、`/fast`：

```
● status
● mcp
● fast
  fast: unavailable for current model
```

`/fast` 的正文正常显示；`/status` 和 `/mcp` 只剩一行标题，正文完全不见，也没有“可展开”的提示。

### 1.2 根因链

1. `commandController.handleStatus / handleMCP`（`pkg/tui/commands.go:1526,1557`）把报告放进一个 `Frame{Kind: FrameSystem}`。报告本身是正确生成的。
2. `collapsibleKind`（`pkg/tui/tty_session.go:1076`）把 `FrameSystem` 和 `FrameContextDebug` 列为可折叠类型。
3. `foldBlock`（`pkg/tui/reducer.go:2593-2601`）对“其他可折叠类型”的通用分支是：行数超过 `foldThreshold`（= `toolOutputMaxLines` = 3）时，默认折叠状态**只返回标题行**，既没有预览，也没有 `… +N lines` 提示。
4. 因此任何超过 3 行的系统报告，默认都只剩 `● <title>`。

折叠来自 `0f293a86`：引入 viewport 时，设计是超过 12 行才折叠；后来阈值被统一成 3 行，系统报告就整类被吞掉了。这同时违反 R-full。

### 1.3 受影响的同类组件

- `/status`、`/mcp`；未知命令时的命令目录（`handleCommandCatalog`）
- `/permissions` 的文本回复；`/skills` 列表和导入结果；`/migrate` 和 `/sandbox` 在非 TTY 下的文本输出
- `run.go:3903`（`applySlashOutcome`：所有经共享执行器处理的命令回复，包括 `/context`、`/diff`、`/plan show`）
- `commands.go:387`（恢复会话时的报告帧），以及 `commands.go:1719/1759/1782`

### 1.4 修复

不变量：**用户主动请求的报告必须完整可见，长内容靠滚动而不是折叠。**

- 从 `collapsibleKind` 中移除 `FrameSystem`。
- **`FrameContextDebug` 不能只是简单地取消折叠**：viewport 用 `fullBodyMode = true` 渲染所有帧（`renderFrameLines`），而 `contextDebugBody(view, full=true, raw)` 在这种模式下会返回**原始 JSON**。取消折叠之后，默认显示的就会是 JSON 原文，违反 R-json。处理方式：
  - 第 0 步先确认 `FrameContextDebug` 在生产中是否还有生产者。它只在“system 消息内容是带 `context_pressure` 的快照 JSON”时才会生成；`/context` 本身输出的是格式化文本，走的是 `FrameSystem`（`run.go:3903`）。
  - 如果没有生产者：整个 `FrameContextDebug` 类型及其渲染、解析、摘要函数全部删除。
  - 如果有生产者：取消折叠，并删除 `fullBodyMode` 下返回原始 JSON 的分支，始终渲染语义化的正文。
- 移除之后，剩下的可折叠类型（Thinking、Tool、MemoryCompact）都有自己专用的分支。`foldBlock` 末尾那个“只返回标题”的通用分支、`foldThreshold` 常量、`isClickableRow` 末尾的 `return isHeader`，以及 `toggleAtScreenRow` 注释里“error/plan/system 只能点标题”的说法，都会变成死代码或错误文档，一并删除。MemoryCompact 用到的“非工具帧标题回退”分支保留。
- 不加任何防御代码。

---

## 2. 顺带发现的既有缺陷（全部纳入本期）

| # | 缺陷 | 证据 | 修复（根因） |
|---|---|---|---|
| D1 | **斜杠命令的输出被发给了模型**（owner 已确认是 bug）。两个 surface 的分发层都是“默认持久化，除非命令返回 `Ephemeral`”：会把输入的命令和它的回复作为 user + assistant 消息追加进 transcript，下一轮随请求发给 LLM。TUI 的 `/status`、`/mcp` 走的是原生分支，所以没中招；但是任何经过 `RunSurfaceTurn → slashCommandResult` 的命令都会中招。 | `gateway/server.go:1409`；`tui/chat_surface.go:98-110` | 斜杠命令的回复只是 UI 输出，任何命令、任何 surface 都不写进 transcript（R-model）。具体做法：删除两处持久化分支，同时删除 `turn.Result.Ephemeral` 字段以及所有设置它的地方（不能删 `llm.Message.Ephemeral`，那是另一个东西），不再留一个默认会泄漏的开关。inject-prompt 类命令走 `ShouldContinueRun`，不受影响。 |
| D1b | 斜杠命令的回复被**冒充成助手消息**显示：TUI 的 ephemeral 分支用 `MsgKindAssistant` 发出；Web 为斜杠回复伪造了一整套 run 生命周期（`run_started` → `delta` → `run_completed`，run id 为 `slash-…`），显示成一个助手气泡。 | `tui/chat_surface.go:98-104`；`gateway/server.go:1415-1432` | TUI 统一以 system 报告帧呈现。Web 改为发送一个专用的 `slash_reply` 消息，前端渲染成系统提示卡片，不创建 run，也不产生 “Worked for”。刷新页面后回复消失，这符合 R-model。 |
| D2 | MCP 工具卡片在每次重绘时都用 `slog.Info` 打印完整的工具输出（调试遗留） | `tui/render.go`，`isMCPToolTitle` 分支 | 删除 |
| D3 | 完整输入 `/context` 后回车，执行的却是 `/context-restore` 技能 | 真实 TUI 复现；`run.go:686-691`：浮层激活时，回车会把整行替换成高亮项；而过滤结果按注册顺序排列，没有把精确匹配排在第一位 | 根因在**排序**而不在回车处理：过滤时把名字完全一致的命令排到第一位，高亮默认落在精确匹配上。回车逻辑不变，用户用方向键选别的项时仍按选中项执行 |
| D4 | Skill 卡片里的长路径在 120 列终端下折行错乱（`…/cont` / `ext-rest` / `ore/SKILL.md`） | 真实 TUI 截屏 | 找出双重折行的根因；路径只能在视口宽度处折一次行 |
| D5 | **有斜杠命令的浮层打开时，模型发起的审批会被直接驳回**：`approvalOverlay.Run` 用 `tryAcquireComposerInput` 抢不到输入，就返回 `dismissDecision()`。用户根本没有机会拍板，违反“需要用户拍板的事必须让用户拍板”。把 `/status`、`/mcp` 做成浮层会让这种情况更常见。 | `tui/overlays.go:327-334`（`questionOverlay` 在 `:1936` 同理） | 审批**排队等待**当前浮层释放输入，不再驳回；随上下文取消，不会死锁，因为浮层的持有者并不等待 agent。浮层底部显示“有 1 个待审批请求 · Esc 关闭后处理”。`/status`、`/mcp` 这类只读面板在审批到达时自动让位（关闭）。对所有浮层一起修（R-fix）。 |
| D6 | OAuth 完成后调用 `s.runner().Load()`，在会话中途重载 runner。MCP 段只在服务器列表的指纹变化时才重建，而 OAuth 令牌存在 overlay 文件里，不在列表里，所以这次重载根本不会给本会话带来工具；只会白白重建 LLM 客户端和 guardian，而且让人误以为认证后工具会立刻出现。如果以后指纹纳入了认证状态，就会直接在会话中途改动工具表。 | `tui/chat_slash.go:1490-1492`；`run/runner.go:820-825` | 删除这次 `Load()`。第 0 步确认：已注册的服务器在使用时重连，会重新读取 overlay 里的新令牌。认证结果按真实语义告诉用户（§5.3）。 |
| D7 | 用量没有按“每次请求”完整记录：`fb_messages.usage_json` 存的是 `LastResponseUsageCopy()`（一轮里**最后一次**模型调用）；`fb_runs` 只存 `InputTokens`/`OutputTokens`，缓存读写的数量从未持久化。而且 OpenAI 的 `InputTokens` 已经减去了缓存部分，所以现在 `/status` 显示的 “prompt” 实际上只是**未命中缓存的输入**，严重偏小。 | `turn/session.go:351-388`；`run/supervisor.go:328-340`；`llm/openai/responses_llm.go:312-319` | `fb_runs` 新增 `usage_cache_read_tokens`、`usage_cache_creation_tokens` 两列，由 `persistRunUsage` 从本次 run 的累计用量（`Summary.Usage`，包含 cache 字段）写入。按 no-migration 规则直接按最终形态建表。会话用量 = 该会话所有 run 的合计（§4.3）。 |
| D8 | 参数提示与解析器不一致：`/model` 在 TUI 拒绝任何参数，提示却写着 `[model-name] [effort]`；`/compact` 拒绝任何参数，提示却写着 `[reason]`；`/agent`、`/subagents` 拒绝参数，提示却写着 `<agent-name>`；`/fast` 的提示漏了 `status`；`/plan` 的提示 `[on\|off]` 与解析器接受的 `mode/set/show/<description>` 对不上；`/mcp` 的提示只有 `[verbose]` | `turn/slash.go:74-98`，以及各命令的解析器 | 见 §6：精简语法，并用测试保证提示与解析器保持一致 |
| D9 | `/context` 在没有快照时返回字面量 `{}`（原始 JSON，违反 R-json）；`FormatContextSnapshot` 的 `full` 参数所有调用方都传 `false`，是死参数 | `turn/context.go:42-44,54-60` | 没有快照时显示一句话 `No context snapshot yet — it is recorded after the first turn`；删除 `full` 参数和原始 JSON 分支 |
| D10 | Web 的斜杠命令下拉框把内部字段 `actionKind`（`immediate`、`open-panel`）显示给用户 | `ForebrainPromptTextarea.vue:621` | 下拉项只显示描述和参数提示（§6.3） |
| D11 | `tool.RenderNotification` 的 `typ` 总是以 `tool_` 开头，所以 `ToolLike` 恒为真；`notify.go:386` 的 `MsgKindSystem` 分支和 `TranscriptRole: "system"` 都是死代码 | `tool/notification.go:161-181`；`tui/notify.go:386` | 删除 `ToolLike` 字段和两处死分支 |

---

## 3. 交互形态与面板运行时

两个命令都做成**交互式面板**，覆盖在输入区上，transcript 仍在上方可见。视觉上沿用 forebrain 现有浮层的标记符号、gutter 和配色：标签用强调色；值与助手正文同亮度，禁止 dim；不用粉色，失败统一用红色 `✗`。

### 3.1 面板由主循环驱动，不是阻塞式模态

现在的 `ShowInfo` / `Select` 都是 `rawSelector` 在**主 UI goroutine 上直接阻塞读 stdin**，有两个后果：

1. 面板打开期间，主循环不再处理通知，正在进行的 run 的流式输出在上方停住，违反“助手消息必须逐字流式”；
2. 审批抢不到输入就会被驳回（D5）。

因此 `/status` 和 `/mcp` 的面板**不沿用阻塞式 selector**，而是做成主循环持有的 composer 状态（与现有的斜杠补全浮层 `SlashOverlay` 是同一种模式）：

- 按键从主循环已有的 `inputEvent` 流进入面板的 reducer；
- 主循环照常处理 UI 通知，transcript 继续流式更新；
- MCP 状态变化（`runner.MCPStartup().Subscribe`，回调只负责投递到主循环的通知队列，不阻塞）会触发面板原地重绘；
- 审批 overlay 通过既有的 `acquireInteractiveInputPause` 接管输入时，面板收起，审批结束后恢复。

### 3.2 通用行为

- **宽度与高度**：按当前宽度排版，resize 时重新排版；值太长时在值这一列内折行（R-full）。内容超过面板高度时在面板内滚动（滚轮 / PgUp / PgDn），不能压缩或截断内容。
- **按键**：`Esc` 返回上一级，在顶层时关闭面板；`Ctrl+C` 直接关闭面板；`Tab` 或 `←` / `→` 切换页签；`↑` / `↓` 移动选择；`Enter` 确认。
- **非 TTY**（测试、管道）：把同一份语义结构渲染成纯文本报告帧，只显示，不持久化。

---

## 4. /status

### 4.1 语义

- 只读的“本会话此刻”面板，不进 transcript（R-model）。
- **不接收参数**（R-args）。带参数时回复一句用法提示：`status: /status takes no arguments`。
- 在 side conversation 中调用时，显示的是该 side conversation 自己的会话，页签标题标注 `side`。

### 4.2 布局：Status / Usage 两个页签

```
  Status   Usage

  Version:          v0.0.1
  Session name:     forebrain tui slash命令修复
  Session ID:       d8f83e8d-c9ff-466d-9d60-3412e0d2d282
  Directory:        /Users/doudou/workspace/unionj-cloud/forebrain-harness
  Agent:            main · plan mode (drafting)
  Proxy:            http://127.0.0.1:7897

  Model:            deepseek / deepseek-chat · fast off
  Endpoint:         https://api.deepseek.com
  Permissions:      Workspace · ask on request · 3 rules
  Sandbox:          workspace-write · seatbelt
  MCP servers:      2 connected, 1 failed, 1 disabled · /mcp
  Instructions:     AGENTS.md (agent), FOREBRAIN.md, docs/FOREBRAIN.md
  Config files:     ~/.forebrain/forebrain.yaml, .forebrain/mcp_servers.yaml

  Esc to close · Tab to switch
```

```
  Status   Usage

  Context:          ████████████░░░░░░░░  62% left · 48K of 128K
  Auto-compact at:  102K
  Session:          1.9M input · 42K output · 38 requests
  Cache hit rate:   94% · 1.79M read · 60K written · 51K uncached
  Last turn:        48K input · 1.2K output · 91% cached
  Work:             plan set · 3 of 7 todos done

  Esc to close · Tab to switch
```

- 所有值完整显示，Session ID 不省略（R-full）。
- MCP servers 这一行里各项计数带状态色，只列非零的状态。
- 没有数据的行不显示（没有代理、没有 MCP 服务器、还没有用量、没有 plan/todo）。字段不能显示 `-` 或 `0` 这类占位值。
- Instructions 里，项目下存在 FOREBRAIN.md 但项目**未受信任**时，显示为 `FOREBRAIN.md (not loaded: project not trusted)`；指令正文被截断时，显示 `(truncated to 120K chars)`。
- Skill offer 行只在该功能被关闭时出现：`Skill offer: off (features.skill_offer)`。

### 4.3 数据来源

| 字段 | 来源 | 注意 |
|---|---|---|
| Version | `home.Version` | |
| Session name / ID | session store | |
| Directory | 会话的项目根目录 | Web 也用会话的项目根，而不是 gateway 进程的 cwd |
| Agent | 主 agent id + `state.Get` 的 Mode/Phase | Phase 只在 plan 模式下显示 |
| Proxy | 进程环境 `HTTPS_PROXY` / `HTTP_PROXY` / `ALL_PROXY` | 去掉 userinfo |
| Model / Endpoint | `run.PrimaryModel`、provider 的 base_url、fast 状态 | Endpoint 去掉 userinfo 和 query |
| Permissions | `safety.MatchApprovalPreset`，与 `/permissions` 选择器使用同一套命名 | 匹配不到预设时显示 `Custom · <approval> · <sandbox>`，不能硬套一个预设名 |
| Sandbox | 与 `/sandbox` 报告同源的一行摘要 | |
| MCP servers | §5 的 `MCPInventory` 计数 | 与 `/mcp` 同源，两处数字必然一致 |
| Instructions | **新增**：`assembly` 的规则缓存在计算合并正文时，同时记录来源文件列表、每个文件的信任状态和是否被截断，与正文存在同一个缓存键下 | **合并正文的字节必须完全不变**，用 golden 测试守护（R-cache） |
| Config files | 配置加载器实际读取的文件列表 | |
| Context / Auto-compact | 与底部状态栏同一个函数、同一个输入（`CalculateTokenBudgetWithOptions`，输入为上一次响应的总 prompt 量） | 两处数字必然一致 |
| Session / Cache hit rate | **新增**（D7）：对本会话**及其子代理会话**的 `fb_runs` 做 SUM(input, output, cache_read, cache_creation)，requests 是 run 内的模型调用次数 | 命中率 = `CacheRead / (CacheRead + CacheCreation + Input)`。第 0 步确认 compaction 和记忆提取的模型调用是否记入所在 run；没记入的要单独记录，不能漏算 |
| Last turn | 最近一个已完成 run 的上述四项 | 输入 = uncached + read + written |
| Work | plan 是否存在 + todo 完成数 / 总数 | |

---

## 5. /mcp

### 5.1 语义

- 管理面板：展示本会话**冻结的有效服务器列表**和实时状态，并可以进入详情执行操作。不进 transcript。
- **不接收参数**（R-args）。认证、工具、资源、详情都在面板里完成。原来的 `/mcp auth <server>` 也删除：面板里的 Authenticate 已经覆盖，保留它就违反“只保留必需参数”（§11 说明了对原决策 3 的调整）。

### 5.2 列表页

```
  Manage MCP servers
  5 servers

  Project MCPs (/Users/doudou/workspace/unionj-cloud/forebrain-harness/.forebrain/mcp_servers.yaml)
❯ caveman-shrink · ✗ failed

  Global MCPs (~/.forebrain/forebrain.yaml)
  codegraph · ✓ connected · 1 tool
  notion · △ needs authentication
  linear · ⠋ connecting…
  search · ○ disabled

  Not in effect: foo — command not found
  MCP config changes on disk take effect in a new session
  Error logs: ~/.forebrain/logs/error.log
  ↑/↓ to navigate · Enter to open · Esc to close
```

- 按作用域分组，组名带配置文件的实际路径。组标题不可选中，光标会跳过它。
- 状态：connected ✓ 绿；failed ✗ 红（`ConnStatusError`）；connecting 转圈，灰；needs authentication △ 黄（`AuthStatusNeedsAuth`）；idle ○ 灰（空闲时释放了连接，使用时自动重连）；skipped – 灰（启动时用户按了 Esc）；disabled ○ 灰；disabled 但本会话仍在运行时显示 `✓ connected · disabled from next session`。
- 错误日志路径取自实际的 forebrain home（`FOREBRAIN_HOME`），不能写死 `~/.forebrain`。
- 没有任何服务器时，显示 `No MCP servers configured`，并给出两个配置文件的位置。

### 5.3 详情页（Enter 进入，Esc 返回）

```
  codegraph MCP server

  Status:     ✓ connected · 1 tool
  Scope:      global · ~/.forebrain/forebrain.yaml
  Transport:  stdio
  Command:    codegraph serve --mcp
  Env:        CODEGRAPH_DB (value hidden)
  Auth:       none

❯ View tools
  Disable (takes effect in a new session)

  ↑/↓ to navigate · Enter to confirm · Esc to go back
```

- http 服务器显示 `URL:`，URL 去掉 userinfo 和 query。header 和 env 只显示键，值一律隐藏。
- failed 的服务器显示 `Error:`，内容是服务器自己的原始报错文本：一句话、完整显示，不套 JSON 外壳（R-json）。
- 操作项只列出当前能用的：
  - **View tools**：列出本会话**实际暴露给模型的工具**，数据来自冻结的工具表：按 `mcp__<NormalizeName(server)>__` 前缀归属到服务器。列表只显示工具名；在某个工具上按 Enter 进入工具详情，显示**完整描述**和**参数表**（名称、类型、是否必填、说明、枚举值和默认值；嵌套对象缩进展开），绝不显示 schema 的 JSON（R-json、R-full）。不再临时连接服务器去 `ListTools`，现有那个会打出 “no connected MCP session” 的路径随之删除。
  - **View resources**：只在连接存活且服务器声明了资源时出现，按名称和 URI 列出。
  - **Authenticate**：只对配置了 OAuth 的服务器显示，复用现有的本地 OAuth 流程，面板内原地显示进度和结果。结果按真实语义说明（D6）：服务器本会话已注册的，显示 “Authenticated · used on next reconnect”；本会话因为未认证而没有注册的，显示 “Authenticated · tools available in a new session”。
  - **Disable / Enable**：见 §5.4。标记为 `required: true` 的服务器不提供 Disable，改为显示 `Required by config`。

### 5.4 会话内不重连，启用/禁用在新会话生效（R-cache）

- 工具表在会话首次请求之前就已冻结（`MCP_STARTUP_NONBLOCKING_PLAN.md` §6）。启动时失败的服务器，即使现在重连成功，本会话也拿不到它的工具；而中途改动工具表会让整个缓存前缀失效。因此**不提供 Reconnect**。
- **Disable / Enable 的存储与技能开关完全一致**：写入当前主 agent 工作区根目录下的 `state/mcp/disabled.json`（与 `state/skills/disabled.json` 同一种机制，按主 agent 隔离，集群语义也与技能开关相同）。键为 `scope + 服务器名`，project 作用域的服务器再加上项目根。
  - **不改写用户的 YAML**：改写会丢失注释和格式，还可能改变 project MCP 的 consent 指纹（`ServerFingerprint`），导致重新请求授权。
  - 在 `mcp.MergeSessionServers` 之后过滤掉被禁用的条目。只有新会话在组装时才读取这个文件；本会话冻结的列表和工具表不变。被过滤的条目连同作用域一起交给 `MCPInventory`，以 disabled 状态显示。
- 操作之后，面板原地显示 `disabled from next session` 或 `enabled from next session`。

---

## 6. 斜杠命令内联参数精简与提示（全命令，R-args）

### 6.1 原则

一个内联参数只有在以下情况才保留：它是命令完成本职工作所必需的**自由文本**，没有任何选择器能够提供（标题、目标、任务描述、diff 基线、路径）。凡是在已知选项里做选择的（模型、会话、agent、开关、服务器、详细程度），都交给选择器或面板。

### 6.2 逐命令核对

| 命令 | 现在的提示 | 解析器实际行为 | 精简后 | 理由 |
|---|---|---|---|---|
| `/model` | `[model-name] [effort]` | TUI 拒绝任何参数 | 无参数 | 选择器 |
| `/fast` | `[on\|off]` | on/off/status 及同义词，另有 toggle | 无参数，切换 | 状态在底部状态栏和 `/status` 里可见 |
| `/permissions` | `[explain <tool>]` | 无参数→选择器；help、explain | `[explain <tool>]` | 工具名是自由文本，面板里没有等价功能；删除 `help` |
| `/review` | `[diff-base]` | 注入提示 | 保留 | 自由文本 |
| `/rename` | `<title>` | | 保留 | 自由文本 |
| `/resume` | `[session-id]` | | 无参数 | 有选择器；需要脚本化时用 CLI `forebrain resume <id>` |
| `/compact` | `[reason]` | **拒绝任何参数** | 无参数 | 修正提示 |
| `/context` | `[reset-snapshot\|compression]` | compression 在共享层直接返回 unavailable | 无参数 | compression 的统计并入 `/context` 的输出；reset-snapshot 删除（快照按 run 记录，第 0 步确认删除后没有功能损失） |
| `/plan` | `[on\|off]` | mode on/off、set、show/status、`<description>` | `[description]` | 退出 plan 模式用已有的快捷键（`hotkeyTogglePlanMode`）或审批流程；show 并入 `/status` 的 Work 行；set 删除（plan 由模型写入） |
| `/agent` | `<agent-name>` | TUI 拒绝参数 | 无参数 | 选择器 |
| `/subagents` | `<agent-name>` | 拒绝参数 | 无参数 | 选择器 |
| `/diff` | `[path]` | | 保留 | 自由文本 |
| `/goal` | `<objective>` | | 保留 | 自由文本 |
| `/migrate` | `[claude\|codex] [--home …] …` | 参数齐全时跳过选择器 | 无参数 | 选择器流程已经完整；没有 CLI 依赖这个参数形式 |
| `/status` | `[verbose]` | verbose | 无参数 | §4 |
| `/mcp` | `[verbose]` | verbose/tools/resources/auth | 无参数 | §5 |
| 技能 `/<skill>` | `[request]` | | 保留 | 自由文本 |

对 Web 也一样：Web 通过同一个共享执行器解析，语法一致。

### 6.3 提示的显示

- **TUI**：保留现有的 composer 行内提示（`showArgumentHint`）。另外在补全下拉的每一行、命令名之后，也显示参数提示，让用户在选之前就能看到。不带参数的命令不显示提示，并且 `SupportsInlineArgs` 设为 false。
- **Web**：下拉项显示“描述 + 参数提示”，删除 `category · actionKind`（D10）。输入完整命令名和空格之后，在输入框里显示与 TUI 相同的占位提示。
- **单一事实来源**：新增表驱动测试，对每个 `SupportsInlineArgs` 命令，用提示里的示例调用解析器，结果不能是用法错误；对每个无参数命令，带任意参数调用都必须返回用法提示。这样提示和解析器不可能再次漂移。

---

## 7. 架构

- **共享语义层**（`pkg/turn`）：`BuildStatusReport(src) StatusReport`、`BuildMCPInventory(src) MCPInventory`，两个 surface 共用。替换现有的 `FormatStatusSummary` 和 `FormatMCPServersScoped*` / `FormatMCPRuntimeSnapshot*` 一族（全部删除）。另有一个 Markdown 渲染器，供 Web 和非 TTY 使用；Web 端的 `/status` 输出按 Status、Usage 两节。
- **TUI**：面板状态由主循环持有（§3.1），代码放在 `run.go`（状态与按键）和 `render.go`（绘制）。`handleStatus` / `handleMCP` 只负责打开面板。
- **Web**：斜杠回复用 `slash_reply` 消息，按系统提示卡片渲染（D1b）。`McpView.vue` 对照 `MCPInventory` 补齐：作用域分组、认证状态、错误原文、工具列表和工具详情、Disable / Enable。新增的 gateway API 复用与 TUI 相同的服务函数。
- **存储**：`fb_runs` 新增两个缓存列（D7）；新增 `state/mcp/disabled.json`（§5.4）。
- **包文件数**：`turn`、`tui`、`gateway`、`state` 都已达到 20 个生产文件的上限，**不新增文件**；`mcp` 包目前 17 个，禁用存储放在 `mcp/project_config.go` 旁边的现有文件里。
- **命令元数据**（`turn/slash.go`）：按 §6.2 更新 `SupportsInlineArgs` 和 `ArgumentHint`；`/status`、`/mcp` 的动作类型改为 `open-panel`。Web 不依据 `actionKind` 做行为分支，已核对。

---

## 8. 开发计划（按里程碑提交到 main）

**M0 — 折叠根因 + 小缺陷**
1. `collapsibleKind` 移除 `FrameSystem`；按第 0 步的结论删除或修正 `FrameContextDebug`；删除 `foldBlock` 的通用分支、`foldThreshold` 和相关的点击分支及注释。
2. D2、D9、D11。
3. 测试：超过 3 行的系统帧完整渲染、不能点击；已有的 tool / thinking / compact 折叠测试保持通过；任何渲染模式下都不出现 JSON。
4. 真实 TUI 验证：`/context`、未知命令目录、`/permissions explain` 的正文都完整可见。

**M1 — R-model（D1 / D1b）**
1. 删除两处持久化分支和 `turn.Result.Ephemeral`；TUI 统一用 system 报告帧；Web 改用 `slash_reply`。
2. 回归测试：两个 surface 上逐个执行全部命令，DB 中 `source='transcript'` 的行数不变；紧接着发一轮普通消息，请求体中不含任何斜杠回复。

**M2 — 共享语义层 + 数据**
1. 第 0 步核对：`FrameContextDebug` 的生产者；compaction 和记忆提取调用的用量归属；配置文件列表的来源；已注册服务器重连时是否重新读取 OAuth overlay；`reset-snapshot` 删除后是否有功能损失。
2. D7：两个新列 + `persistRunUsage` + 会话汇总查询（含子代理会话）。
3. `assembly` 规则缓存记录来源文件信息，合并正文的字节不变（golden）。
4. `StatusReport` / `MCPInventory` 的构建函数 + Markdown 渲染；删除旧的格式化函数；Gateway 改为调用同一组函数。
5. 单元测试：MCP 各种状态组合与分组、空字段省略、命中率、`Custom` 权限预设、URL 脱敏。

**M3 — 面板运行时 + D5**
1. 主循环持有的面板状态：按键 reducer、通知泵、MCP 状态订阅、审批让位。
2. D5：审批与提问浮层改为排队等待输入，不再驳回；所有浮层底部显示待审批提示。
3. 测试：面板打开期间，流式输出继续写入 transcript；面板打开时来审批，审批能正常弹出并被批准；键盘导航。

**M4 — /status 与 /mcp 面板**
1. `/status` 的两个页签。
2. `/mcp`：列表、详情、工具列表与工具详情（参数表）、资源、Authenticate（含 D6）、实时刷新。
3. Disable / Enable：`state/mcp/disabled.json`、合并后过滤、面板操作项、`required` 服务器的处理。
4. Golden 测试：宽度 120 / 80 / 50；覆盖所有状态；Disable 后本会话的工具表与前缀字节不变，新会话中该服务器既不启动也不注册工具。

**M5 — R-args（§6）+ D3 / D4 / D10**
1. 按 §6.2 精简各命令的解析器和元数据，删除对应的死分支；表驱动的一致性测试。
2. TUI 下拉行显示提示；Web 占位提示和下拉文案。
3. D3 精确匹配排在第一位；D4 双重折行。

**M6 — Web 对等、收尾、验证**
1. `McpView` 字段补齐与 Disable / Enable API；Web 端的 `/status`、`/mcp` Markdown 输出。
2. `deadcode` 扫描并清除；`go test -tags fts5 ./pkg/...`（包括 `pkg/architecture`）和前端测试。
3. 真实 TUI 验证（`run-forebrain`）：配置一个可用的 stdio MCP 服务器、一个必然失败的服务器和一个 OAuth 服务器，截屏确认：状态实时变化、详情与工具参数表、Disable 在新会话生效、面板打开期间流式输出继续、面板打开时来审批不会被驳回、终端缩窄后的排版。
4. 真实模型验证：只在 DeepSeek 和 OpenAI 上做，跑几轮对话；Usage 页的数字要与 DB 里手算的结果一致。没有凭据就跳过，并如实说明。
5. **缓存实测（R-cache）**：
   - 在 DeepSeek 和 OpenAI 上，用同一段脚本化对话，在改动前后分别测 `CacheRead / (CacheRead + CacheCreation + Input)`，要求改后 ≥ 改前。
   - 用前缀字节 golden 核对：规则缓存的改动、disabled 过滤（存储为空时）、面板的所有操作，都不改变 tools + system 的字节。
   - D1 会让新会话的 transcript 不再包含斜杠回复，只影响尾部追加。

---

## 9. 风险与对策

| 风险 | 对策 |
|---|---|
| 主循环驱动的面板改动了输入路径，可能影响现有浮层 | 只有 `/status`、`/mcp` 迁到新运行时，其余浮层不动；D5 对阻塞式浮层单独修；输入相关的测试全部跑一遍 |
| 审批从“驳回”改为“排队”可能引入死锁 | 浮层的持有者从不等待 agent，排队随 ctx 取消；加 `-race` 测试和“面板打开时来审批”的集成测试 |
| 去掉 `Ephemeral` 之后，某个命令确实需要进入 transcript | 需要模型参与的命令只走 `ShouldContinueRun`；逐个核对所有 `Ephemeral` 为 false 的命令，确认没有任何一个需要被模型看到 |
| 删除内联参数会让熟悉旧语法的用户输入失败 | 用法提示是一句话，直接说明应该怎么做（例如 “/model takes no arguments — pick in the list”）；不保留兼容语法 |
| 规则缓存记录来源信息时不小心改变了合并正文 | 在同一个缓存键下一并计算；golden 断言字节完全一致 |

---

## 10. 已确认的决策

1. `/status` 保留 **Status / Usage 两个页签**。
2. 缓存命中率只能升不能降：会话内不提供 Reconnect；Disable / Enable 只在新会话生效；任何面板操作都不改变本会话发往模型的前缀；M6 做实测。
3. 删除 `/status verbose` 和 `/mcp verbose|tools|resources`（见 §11 对 `auth` 的调整）。

## 11. 本次深度评审对方案的修订摘要

- §1.4：`FrameContextDebug` 取消折叠后会显示原始 JSON，所以要么删除这个类型，要么只渲染语义化正文。
- 新增 D1b、D5–D11，都已对照代码核实。
- §3：面板由阻塞式 selector 改为主循环驱动，解决流式输出停住和审批被驳回的问题。
- §4.3：用量来源从 `usage_json`（只记录最后一次调用）改为扩展后的 `fb_runs`；Instructions 的来源信息必须随规则缓存一起记录。
- §5.3：认证结果按真实语义显示；工具列表不截断描述；参数显示为参数表，而不是 JSON。
- §5.4：Disable 的存储从“改写 YAML”改为与技能开关相同的按 agent 存储的状态文件。
- §6：按 R-args 精简全部命令的内联参数并保证提示一致。对原决策 3 的调整：**`/mcp auth <server>` 也删除**，因为面板里的 Authenticate 已经覆盖，保留它违反“只保留必需参数”。
- §8：开发计划按上述修订重排；缓存验收改为前后对比的实测数字加前缀 golden。
