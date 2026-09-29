# LSP 代码智能接入规范：给 forebrain 加上语言服务器的语义与功能

> 日期：2026-09-29（定稿）
> 状态：**已定稿，可实施**。§15 的全部决策已定，实施按 [`docs/plan/lsp/`](lsp/README.md) 的 18 个任务计划分批交付
> 基线：`main` / `6305ea9`
> 范围：新增 `pkg/lsp`；改动 `pkg/config`、`pkg/event`、`pkg/tool`、`pkg/safety`、`pkg/run`、`pkg/turn`、`pkg/process`、`pkg/tui`、`pkg/gateway`、`frontend`、`pkg/migrate`、`cmd/forebrain`、`pkg/architecture`、`.github/workflows/lsp-integration.yml`（新）
> 对标：Claude Code 的 code intelligence 插件（`gopls-lsp`、`jdtls-lsp`、`rust-analyzer-lsp` 等）、`LSP` 工具、编辑后诊断、**LSP plugin recommendation** 对话框
> 需求：必须支持 Java、Go、Rust、C、C++、Kotlin、Swift、Scala、C#、TypeScript、PHP、Python 等主流语言，且不限于此

---

## 给实施者（先读这一节）

1. **本文件是规范**。「必须 / 不得」是硬性要求；「应」是默认做法，偏离时要在 PR 描述里写理由。规范与代码现状冲突时，**不要自行改规范**，按任务计划的 STOP 条件停下汇报。
2. **实施入口是任务计划**：[`docs/plan/lsp/README.md`](lsp/README.md) 列出 18 个任务的顺序、依赖和状态。每个任务计划自带现状代码摘录、范围、逐步操作与验证命令、完成判据和 STOP 条件，可单独交给一个模型执行。任务计划引用本规范时写作「规范 §x」。
3. **附录 B（模型可见文本）与附录 C（错误消息）必须逐字使用**，它们进入 prompt 前缀或工具结果，改一个字节都会影响缓存和测试。
4. 仓库通用规则（摘自 `FOREBRAIN.md`，每个任务都适用）：
   - CGO 必开：`CGO_ENABLED=1 go test -tags fts5 ./... -count=1`；`go vet ./...`；`gofmt -l cmd pkg third_party` 必须为空。
   - 改了 `pkg/` 下任何 Go 生产代码，都必须运行 `scripts/package-graph.sh` 并提交 `pkg/architecture/testdata/graph.json`（它记录 import 关系和每个包的代码行数，CI 会 `git diff --exit-code`）。
   - 每个包必须有准确的 `doc.go`。
   - 提交：Conventional Commits 标题 + `.gitmessage` 的 `Why:` / `What:` / `Verification:` 正文 + `git commit -s`（DCO）。标题用 `scripts/check-commit-message.sh` 自检。
   - **不得提交 `pkg/gateway/dist`**：`cd frontend && pnpm build` 会写入该目录，提交前执行 `git checkout -- pkg/gateway/dist`。
   - LLM 可见的 prompt 文本必须 provider-neutral、字节稳定。
5. `pkg/architecture` 的结构测试（§3.14）是硬约束，本规范的文件落点都是按它们定的：
   - 每个包**最多 20 个生产文件**；`tool`、`run`、`process`、`turn`、`tui`、`gateway`、`safety` 已经是 20 个，**这些包里不得新建生产文件**，只能改已有文件。
   - `pkg/` 下**不得有二级包**（只有 `llm/anthropic`、`llm/openai` 例外），所以 `pkg/lsp` 不能有子包；`testdata` 下也不能放 `.go` 文件（结构测试会把它当成包）。
   - 测试文件必须以它覆盖的生产文件命名：`X_test.go` 旁边必须有 `X.go`。
   - 每个包都必须被模块里的其他包导入（测试文件也算）。
   - import 分组：标准库在前。
   - `Runner` 的方法数有棘轮（`TestRunnerOnlyShrinks`）：本规范不给 `Runner` 加方法，界面通过 `Deps` 的提升字段访问 LSP。

---

## 0. 一页结论

| # | 决策 | 要点 |
|---|---|---|
| D1 | **内置语言服务器目录 + 配置，不做插件市场** | forebrain 是单一二进制，没有插件系统。把「哪个命令启动哪个服务器、处理哪些扩展名、根目录怎么找、怎么安装」做成 `go:embed` 的目录，首发覆盖需求点名的 12 门语言，再扩到 20+ 门。目录没有的语言用 `lsp.servers.<name>` 自定义条目接入，字段与 Claude Code 的 `.lsp.json` 一一对应 |
| D2 | **两种能力** | ① **编辑后诊断**：`write_file` / `edit_file` / `apply_patch` 成功后，在**等待窗口**（§2）内收集这次编辑新引入的错误和警告，附在工具结果里；窗口之后才到的诊断在下一次模型请求时以提醒注入。② **只读 `lsp` 工具**：定义、声明、类型定义、实现、引用、悬停类型、文档符号、工作区符号、调用层级、类型层级、当前诊断 |
| D3 | **服务器进程由 forebrain 托管** | 懒启动；按「(服务器, 配置指纹, 工作区根)」一个实例，进程级池共享；空闲回收；崩溃退避重启；关闭时清理**整棵进程树**；**永远不在 `Runner.Load` 路径上启动** |
| D4 | **安全闸门** | 只在**受信任项目**启动；目录服务器必须由用户**显式启用一次**；项目级条目逐条同意（指纹）；工具只读，`workspace/applyEdit` 一律拒绝；带 `file_path` 的 `lsp` 调用在权限层按 `Read` 评估 |
| D5 | **prompt 缓存不动** | `lsp` 工具是否注册在会话冻结时决定；描述、schema 是常量；不改系统提示；服务器启停、推荐接受、配置重载都不改工具表 |
| D6 | **分层不破** | port 接口在 `pkg/tool`，DTO 在 `pkg/event`，实现在新包 `pkg/lsp`（Layer 2），只有组合根 `pkg/process` 与 `cmd/forebrain` 导入它 |
| D7 | **推荐（截图那个框）** | 本会话第一次编辑某语言文件、该语言有目录服务器且未启用时推荐：启用 / 安装并启用 / 暂不 / 此服务器永不 / 关闭全部推荐。**接受后本会话立即获得诊断**。连续 5 次「暂不」自动关闭推荐（替代 Claude Code 的 30 秒超时计数，理由见 §10.2） |
| D8 | **分批交付** | 18 个任务（P0 底座 9 个、P1 模型能力 2 个、P2 产品化 7 个），每个任务一个 PR；P3 的沙箱、rename 预览等列为后续，另立计划 |

---

## 1. 目标、非目标与完成定义

### 1.1 用户可见的结果

1. **编辑后诊断**：agent 改 `invoice.go` 引入类型错误，编辑卡片下出现 `Found 2 new diagnostic issues in 1 file`（可展开）；模型在同一个工具结果里拿到明细，不跑 `go build` 就能改正。
2. **按符号导航**：模型用 `lsp` 工具查定义、查引用、看类型，而不是用 shell 里的 `grep` 找文本。
3. **推荐对话框**：首次编辑 `.go` 文件、`gopls` 已装但未启用时推荐启用；`gopls` 没装但 `go` 在 PATH 上时推荐「安装并启用」。
4. **`/lsp` 面板**（终端与 Web 同一套数据）：每个服务器的状态、启用停用、重启、日志、安装、重新开启推荐。
5. **`forebrain lsp doctor`**：检查二进制、版本、根标记、环境变量，并做一次真实握手，打印修复建议。
6. **`/migrate`** 把 Claude Code 里已启用的 LSP 插件对应的服务器启用或导入。

### 1.2 语言覆盖

**必选（任务 06 首发）**

| 语言 | 默认服务器 id | 备选（任务 17 加入目录，低优先级） |
|---|---|---|
| Java | `jdtls` | — |
| Go | `gopls` | — |
| Rust | `rust-analyzer` | — |
| C / C++（含 ObjC、CUDA） | `clangd` | `ccls` |
| Kotlin | `kotlin-lsp` | `kotlin-language-server` |
| Swift | `sourcekit-lsp` | — |
| Scala | `metals` | — |
| C# | `csharp-ls` | — |
| TypeScript / JavaScript | `typescript-language-server` | `vtsls`、`deno`（Deno 项目优先） |
| PHP | `intelephense` | `phpactor` |
| Python | `pyright` | `basedpyright`、`pylsp` |

**扩展（任务 17）**：Ruby、Lua、Dart、Elixir、Zig、Haskell、OCaml、Bash、Vue、Svelte、Terraform、Clojure、Erlang、Nix、Gleam、YAML、Dockerfile；诊断型服务器 ruff、ESLint、Biome。

**「不限于」的兜底**：任意 LSP 服务器都能用自定义条目接入（§5.1）。

### 1.3 非目标

- 不内置、不偷偷下载语言服务器二进制；安装只在用户确认后执行（§6.5）。
- 不做补全、语义高亮、inlay hints、格式化。
- 不让 LSP 改文件：不声明 `applyEdit`，拒绝 `workspace/applyEdit`，不发 `workspace/executeCommand`。
- 只用 stdio 传输。
- 通道会话（Telegram 等）的界面不显示推荐；没有项目根的会话不启动 LSP。

### 1.4 完成定义（全部任务完成后）

| 能力 | 验收 |
|---|---|
| 必选语言 | 任务 12 的集成测试对 gopls、pyright、typescript-language-server、rust-analyzer、clangd 断言 definition / references / hover / document_symbols 有结果，并完整走一遍「编辑引入错误 → 工具结果带诊断 → 修复 → 诊断消失」；C++ 由 clangd 在同一作业里覆盖；其余 6 个服务器（Java、Kotlin、Swift、Scala、C#、PHP）在 nightly 作业里跑同样用例 |
| 首帧不受影响 | 任务 08 的测试：`prewarm` 一个 `initialize` 永不返回的假服务器时，`NewManager` 立即返回 |
| prompt 前缀字节稳定 | 任务 10 的测试：启用/停用/重启服务器、配置重载前后 `Runner.LoadedTools()` 序列化字节完全相同 |
| 不留孤儿进程 | 任务 05 的测试：服务器再起孙进程，`Close` 后两者都消失 |
| 包图 | `pkg/architecture` 测试全绿；`graph.json` 无差异 |
| 安全 | 未受信任目录不启动、不推荐、不注册工具；项目条目未同意不启动；`workspace/applyEdit` 被拒 |

---

## 2. 术语表

| 术语 | 定义 |
|---|---|
| **目录（catalog）** | `pkg/lsp/catalog/*.yaml` 里内置的服务器定义，每条有一个 `id`（如 `gopls`） |
| **服务器配置** | 目录条目 + 全局 `lsp.servers.<id>` 覆盖 + 已同意的项目级覆盖 + 启用状态，合并后的结果（§5.3） |
| **服务器集合** | 当前会话可用的全部服务器配置。可在会话中途变化，因为它不进入 prompt 前缀 |
| **实例** | 一个正在运行的语言服务器进程，键为「服务器 id + 配置指纹 + 工作区根」 |
| **primary / diagnostics 型** | primary 服务器负责导航和诊断，每个文件最多一个；diagnostics 型只贡献诊断，可与 primary 并存 |
| **启用** | 用户允许某个服务器在受信任项目里运行。目录服务器默认**未启用** |
| **冻结** | 会话开始时计算一次、之后不再改变的决定。本规范里只有「是否注册 `lsp` 工具」是冻结的 |
| **等待窗口**（diagnostics wait window） | 编辑工具在**写盘成功之后、返回结果之前**，为收集本次编辑引入的诊断**最多**阻塞的时长，配置项 `lsp.diagnostics.wait_ms`，默认 2500 毫秒。它是**上限而不是固定等待**：诊断一到就返回；没有服务器负责的文件完全不等待；**等满不算出错**——编辑照常成功返回，没等到的诊断转为迟到诊断。完整规则见 §8.3 |
| **基线** | 编辑之前该文件已有的诊断。「新引入的问题」= 编辑后的诊断减去基线（§8.3.4） |
| **迟到诊断** | 等待窗口结束之后才到的诊断，在同一 agent 会话的下一次模型请求时以提醒注入（§8.4） |
| **安静期** | push 型服务器报出目标文件诊断后，再等 300 毫秒收集其他文件的诊断；期间无新推送才结束，且不超过窗口剩余时间 |

---

## 3. 现状与约束（全部来自 `6305ea9` 实测）

### 3.1 仓库里没有任何 LSP 代码，也没有 grep/glob 工具

非测试 Go 代码里搜索 `lsp`、`gopls`、`language server` 均零命中。`pkg/tool/registry.go:85 RegisterDefaultTools` 注册的是 `read_file`、`write_file`、`edit_file`、`shell`、`retrieve_output`、`web_fetch`、`web_search`、`request_permissions` 以及会话/记忆/子代理类工具——**找定义、找引用全靠 shell 里的 `grep`/`rg`**。

### 3.2 工具表 = prompt 前缀，会话内冻结

`pkg/run/runner.go:1323`：「The agent is built once per session and never rebuilt from underneath a turn. Its tool definitions and skills catalog sit in the prompt prefix」。MCP 段用指纹复用保证配置重载不动工具表字节。

### 3.3 包分层与 fan-out 棘轮

`pkg/architecture/cache_test.go`：`packageLayer`（:351，只能导入更低层；表里的包目录必须存在）、`sameLayerEdges`（:526，同层边逐条白名单，**列出不存在的边也会失败**）、`fanOutBudgets`（:169，导入数上限）。

| 包 | 实测 fan-out | 上限 | 结论 |
|---|---|---|---|
| `tool` | 8 | 8 | 不能新增导入 ⇒ port 接口定义在 `tool` 内 |
| `run` | 14 | 14 | 不能新增导入 |
| `tui` | 19 | 19 | 不能新增导入 |
| `gateway` | 18 | 18 | 不能新增导入 |
| `migrate` | 8 | 8 | 不能新增导入 ⇒ Claude 插件解析在 `migrate` 内完成，只写 `config` |
| `turn` | 10 | 12 | 本方案不新增导入 |
| `process` | 18 | 18（目标不设上限） | 导入 `lsp` 后变 19，任务 03 把上限改为 19 |

### 3.4 子进程环境变量白名单

`pkg/home/envguard.go:14` 的 `defaultAllow` 只有 `PATH HOME TMPDIR TMP TEMP LANG LC_ALL TZ USER SHELL TERM`；`Options.ExplicitEnv` 里名字「良性」（只含字母数字下划线）且不像密钥（`looksSecretKey`）的变量会被放行。语言服务器需要 `GOPATH`、`JAVA_HOME`、`CARGO_HOME` 等 ⇒ 目录条目声明 `env_passthrough`，实现时从 `os.LookupEnv` 取值放进 `ExplicitEnv`。

### 3.5 MCP 子进程只杀直接子进程

`pkg/mcp/bridge.go:501` 用 `exec.CommandContext`，无进程组。语言服务器都会再起子进程 ⇒ LSP 必须清理整棵进程树（§7.8）。

### 3.6 读文件观察者存在但生产未接线

`pkg/tool/state.go:1833 SetReadObserver`、`pkg/tool/file_tools.go:117` 的调用点都在，生产代码无人设置（只有 `file_tools_test.go:179`）⇒ 改成可挂多个观察者后复用（任务 11）。

### 3.7 项目级配置的信任 + 同意模型

项目级 MCP 只读 `<project>/.forebrain/mcp_servers.yaml`，受信任且受版本控制的项目才生效，逐条按指纹同意（`pkg/mcp/project_consent.go`），终端启动时在信任提示之后询问（`cmd/forebrain/interactive.go:77` 调 `cmd/forebrain/mcp_consent.go:20`）。

### 3.8 子 Runner 不自动继承 `Deps` 新字段

`pkg/run/factory.go:29 NewIsolatedRunner`（:46 的 `Deps` 字面量）逐个列出继承字段 ⇒ 新字段必须在这里补上继承。

### 3.9 Gateway 按「(agent × 项目)」一个 Runner

`pkg/process/runner_pool.go:645 buildEntryLocked` 为每个项目构建 `run.Deps`；空闲时释放 MCP 连接（`:293`），长期不活跃时驱逐整个 Runner。

### 3.10 提醒注入通道

`pkg/run/config.go:996 reminderAdoptionSink`；`recordReminderAdoption`（:1076）把注入的提醒交给编排循环收进会话；`planReminderMessage`（:1083）把文本包成 `<system-reminder>` 的 IsMeta 用户消息。`wrapSkillOfferLLM`（`pkg/run/skills.go:539`，在 `runner.go:982` 挂接）是范例。

### 3.11 权限层把工具名映射成策略名

`pkg/run/permissions.go:655 permissionToolName`（`shell` → `Bash`）与 `:663 permissionInput`（`read_file` 等取 `file_path`）决定规则匹配；`pkg/safety/engine.go:406 safeReadOnlyTools` 决定无规则时默认放行。

### 3.12 界面既有模式

- 终端：`/mcp` 面板（`pkg/tui/run.go:4494 openMCPPanel`、`panelSession` 接口 :4467）；主循环在 `processUINotification`（`pkg/tui/run.go:239`）里处理通知，`MigrationPreviewMsg` 在此直接弹 `selector` 模态框（:446）——推荐框照此实现。
- 事件：`ChatSession.publishRunEvent`（`pkg/tui/notify.go:688`）把 RunEvent 转为界面消息；Web 在 `frontend/src/composables/useChatStream.ts:1568` 按 `evt.type` 分发（auto-continue 是最近的范例）。
- 工具卡片正文统一由 `pkg/tool/format.go:27 FormatToolStepResult` 生成，两个界面共用。

### 3.14 仓库结构上限（`pkg/architecture/cache_test.go`）

| 测试 | 约束 | 对本方案的影响 |
|---|---|---|
| `TestPackagesStayUnderTwentyProductionFiles`（:1336） | 每包最多 20 个生产文件 | `tool`、`run`、`process`、`turn`、`tui`、`gateway`、`safety` 已满 20：port 与 `lsp` 工具放进 `pkg/tool/search.go`；迟到诊断 wrapper 放进 `pkg/run/config.go`；项目级 LSP 同意放进 `pkg/process/mcp.go`；`/lsp` 文本渲染放进 `pkg/turn/mcp_format.go`；REST 放进 `pkg/gateway/api_extra.go`；`pkg/config` 有 19 个，可新建 `lsp.go`；`pkg/lsp` 规划 17 个文件 |
| `TestPackageShape`（:650） | 包深度 ≤ 1（除 `llm/anthropic`、`llm/openai`）；包总数 ≤ 30；每包 ≥ 2 个生产文件；包名不能是 `common`、`manager`、`utils` 等 | `pkg/lsp` 不能有 `jsonrpc/`、`protocol/` 子包，全部是 `pkg/lsp` 下的文件 |
| `TestEveryPackageHasAnImporter`（:1459） | 每个包都要被其他包导入（遍历含 `testdata`） | `pkg/lsp` 创建的那一刻就必须有导入方 ⇒ 任务 03 同时建包骨架并接入 `pkg/process`；假服务器源码以字符串常量放在测试文件里，照 `pkg/mcp/bridge_test.go:447 processFixtureSource` |
| `TestTestFilesCorrespondToProductionFiles`（:1368） | `X_test.go` 必须对应 `X.go` | 测试写在对应文件的测试里，例如 `lsp` 工具的测试在 `pkg/tool/search_test.go` |
| `TestEveryPackageHasDocGo`（:760） | 每包有 `doc.go` | `pkg/lsp/doc.go` |
| `TestRunnerOnlyShrinks`（:844） | `Runner` 字段 25、方法 39 只降不升 | 新字段加在 `run.Deps`（`Runner` 内嵌 `*Deps`），不加 `Runner` 方法 |
| `TestImportsAreGroupedStdlibFirst`（:1166） | 标准库 import 在前 | — |
| `TestFileNamesHaveAtMostTwoUnderscores`（:1317） | 文件名最多两个下划线 | — |

### 3.13 Claude Code 的做法（官方文档）

- `.lsp.json` 字段：`command`、`extensionToLanguage`（必填），`args`、`transport`、`env`、`initializationOptions`、`settings`、`workspaceFolder`、`startupTimeout`（毫秒）、`shutdownTimeout`（毫秒）、`restartOnCrash`、`maxRestarts`、`diagnostics`。
- 官方插件：clangd-lsp、csharp-lsp、gopls-lsp、jdtls-lsp、kotlin-lsp、liquid-lsp、lua-lsp、php-lsp、pyright-lsp、ruby-lsp、rust-analyzer-lsp、swift-lsp、typescript-lsp。**没有 Scala**。
- `LSP` 工具只读；每次编辑后报告错误与警告；服务器在第一次编辑匹配文件时启动。
- 推荐框：编辑之后出现、每会话一次；30 秒无操作计忽略，5 次后关闭；安装后需重启会话。

---

## 4. 总体架构

```
┌──────────────────────────── Layer 5 ────────────────────────────┐
│ pkg/tui      /lsp 面板 · 推荐模态框 · 诊断行 · lsp 卡片           │
│ pkg/gateway  /api/v1/lsp/* ；frontend: LspView、推荐卡片          │
└───────────────▲─────────────────────────────────────────────────┘
                │ 只经 tool.CodeIntelControl（接口）与 event.LSP*（DTO）
┌─ Layer 4 ─────┴─────────────────────────────────────────────────┐
│ pkg/process  lsp.Pool（进程级）；每个 Runner 一个 lsp.Manager；    │
│              冻结 CodeIntelTool；推荐监听 → RunEvent；             │
│              配置重载 → Pool.Reconcile；关闭                       │
└───────────────▲─────────────────────────────────────────────────┘
┌─ Layer 3 ─────┴─────────────────────────────────────────────────┐
│ pkg/run   Deps.CodeIntel* → AgentToolRuntime；迟到诊断 wrapper；  │
│           权限映射 lsp → Read                                     │
│ pkg/turn  /lsp 命令登记与文本渲染                                 │
└───────────────▲─────────────────────────────────────────────────┘
┌─ Layer 2 ─────┴─────────────────────────────────────────────────┐
│ pkg/tool   search.go（port + lsp 工具）；写后诊断；读观察者        │
│ pkg/lsp    jsonrpc · protocol · position · instance · procgroup · │
│            catalog · resolve · detect · docsync · diagnostics ·  │
│            pool · manager · control · query · project            │
│ pkg/event  lsp.go（DTO + RunEventLSPRecommendation）              │
│ pkg/safety canonical 别名 lsp → LSP；LSP 默认只读放行             │
└───────────────▲─────────────────────────────────────────────────┘
┌─ Layer 1 ─────┴─────────────────────────────────────────────────┐
│ pkg/config  lsp 配置段、features.lsp                              │
└─────────────────────────────────────────────────────────────────┘
```

### 4.1 依赖变化

| 变化 | 由哪个任务完成 |
|---|---|
| `packageLayer` 加 `"lsp": 2`；`fanOutBudgets` 加 `"lsp": {current: 4, target: 4}`；`sameLayerEdges` 加 `"lsp": {"event", "tool"}`；`fanOutBudgets.process` 18 → 19 | 任务 03（建包骨架并接入 `process` 的同一个 PR：包一出现就有导入方，骨架实现 port 所以同时导入 `event` 与 `tool`） |
| `pkg/lsp` 的 fan-out 最终为 4：`config`、`home`、`event`、`tool` | — |
| `graph.json` 每个改 `pkg/` 生产代码的任务都重新生成（含行数） | 各任务 |

### 4.2 port 与 DTO

精确定义在任务 02（port 追加在 `pkg/tool/search.go`，DTO 新建 `pkg/event/lsp.go`）。要点：

- `tool.CodeIntelligence`（给工具用）：`Handles`、`Query`、`DidWrite`、`DidRead`、`DidRunShell`、`PeekLate`、`AckLate`。
- `tool.CodeIntelControl`（给界面用）：`Snapshot`、`Subscribe`、`SetEnabled`、`Restart`、`Install`、`SetRecommendationListener`、`DecideRecommendation`、`ResetRecommendations`。
- `event`：`LSPServerStatus`、`LSPSnapshot`、`LSPRecommendation`、`LSPRecommendationChoice`、`LSPDiagnostic`、`LSPDiagnosticsSummary`、常量 `RunEventLSPRecommendation = "lsp_recommendation"`。

### 4.3 对象归属

| 对象 | 归属 | 生命周期 |
|---|---|---|
| `lsp.Pool` | `process.Environment`（一个进程一个） | `Open` 创建；`Environment.Close` 在关闭 Runner 与 RunnerPool 之后、关闭 SQL 之前关闭 |
| `lsp.Manager` | 每个 Runner 一个（主 Runner 与每个项目 Runner） | 随 Runner 创建；Runner 关闭/驱逐时由 `process` 调 `Manager.Close()` 释放引用 |
| 实例（进程） | `Pool` | 引用归零且空闲、`idle_timeout` 到期、配置指纹变化或进程关闭时关闭 |
| 子代理、fork | 共用父 Runner 的 Manager | 不单独关闭 |

---

## 5. 配置模型

### 5.1 全局：`~/.forebrain/forebrain.yaml`

```yaml
features:
  lsp: true                # 总开关，默认 true；false：不注册工具、不启动、不推荐

lsp:
  recommendations: true    # 推荐，默认 true
  max_servers: 6           # 本进程同时存活的实例上限
  idle_timeout: 600        # 秒：实例无请求、无编辑多久后关闭
  request_timeout: 30      # 秒：一次 lsp 工具调用（含等待索引）的上限
  diagnostics:
    after_edit: true       # 编辑后诊断
    wait_ms: 2500          # 等待窗口上限（§2、§8.3）：诊断一到即返回；等满不算失败，剩余诊断迟到提醒
    min_severity: warning  # error | warning | information | hint
    max_per_file: 20
    max_files: 10
    late_delivery: true
  servers:
    gopls:                 # 目录 id：只写要覆盖的字段
      settings:
        gopls: { staticcheck: true }
    mylang:                # 自定义服务器
      command: /opt/mylang/bin/mylang-ls
      args: ["--stdio"]
      extension_to_language: { ".ml2": "mylang" }
      root_markers: ["mylang.toml"]
```

数值约束（加载时校验，任务 01）：`max_servers` 1–32（0 取默认 6）；`idle_timeout` 60–86400（0 取默认 600）；`request_timeout` 1–600（0 取默认 30）；`wait_ms` 0–30000（**未写时默认 2500；显式写 0 表示不等待**，编辑后诊断全部走迟到提醒）；`min_severity` 只能是四个值之一（空取 `warning`）；`max_per_file` 1–200（0 取 20）；`max_files` 1–50（0 取 10）。`startup_timeout` 0–600 秒、`shutdown_timeout` 0–60 秒、`max_restarts` 0–20。

服务器字段：

| 字段 | 类型 / 默认 | Claude Code 对应 | 说明 |
|---|---|---|---|
| `enabled` | `*bool`；目录条目缺省 false，自定义条目缺省 true | 插件是否启用 | 覆盖层见 §5.3 |
| `command` | string | `command` | 不经 shell；绝对路径或 PATH 上的名字；支持 `~/` 前缀 |
| `args` | []string | `args` | |
| `extension_to_language` | map[string]string | `extensionToLanguage` | 键必须以 `.` 开头，小写 |
| `filenames` | map[string]string | — | 精确文件名 → languageId |
| `root_markers` | []string | — | 支持 `*` 通配（如 `*.sln`） |
| `workspace_folder` | string | `workspaceFolder` | 显式根；相对路径相对项目根 |
| `env` | map[string]string | `env` | 支持 `${VAR}` |
| `env_passthrough` | []string | — | 额外放行的父进程变量名 |
| `initialization_options` | JSON 对象 | `initializationOptions` | YAML 写法同 `llm_providers[].params` |
| `settings` | JSON 对象 | `settings` | 同上 |
| `startup_timeout` | 秒 | `startupTimeout`（毫秒） | 0 取目录值，目录无值取 60 |
| `shutdown_timeout` | 秒 | `shutdownTimeout`（毫秒） | 0 取 5 |
| `restart_on_crash` | `*bool`，缺省 true | `restartOnCrash` | |
| `max_restarts` | int，0 取 3 | `maxRestarts` | 10 分钟滑动窗口 |
| `diagnostics` | `*bool`，缺省 true | `diagnostics` | false：不参与编辑后诊断 |
| `role` | `primary` \| `diagnostics` | — | 缺省 primary |
| `priority` | int | — | 缺省：目录值，自定义条目 50 |
| `prewarm` | bool | — | 会话载入后在后台预启动 |

### 5.2 项目级：`<project>/.forebrain/lsp_servers.yaml`（任务 15）

```yaml
servers:
  gopls:
    settings:
      gopls: { buildFlags: ["-tags=integration"] }
```

- 只在**受信任且受版本控制**的项目生效（与项目级 MCP 同一判断：`safety.TrustedRoot(launch) != "" && launch.Project.VersionControlled`）。
- 项目条目可以覆盖目录服务器的 `args/env/initialization_options/settings/root_markers/workspace_folder`，可以声明新服务器，可以写 `enabled: true` 请求启用。
- **每个项目条目都要逐条同意**，指纹 = 对 `command`、`args`、`env`（键排序）、`initialization_options`、`settings`、`workspace_folder`、`root_markers`、`enabled` 的规范化 JSON 做 sha256。存储 `<agentWorkspace>/state/lsp/project_consent.json`，结构同 MCP 的 `ProjectConsents`（project key → 小写服务器名 → `{fingerprint, decision, decided_at}`）。
- 未同意或拒绝的项目条目完全不生效（包括它对目录服务器的覆盖）。
- `env_passthrough` 与 `priority` 在项目条目里**被忽略**；`${VAR}` 只从 `~/.forebrain/.env` 取值。
- 明文密钥拒收（复用 `config` 包的明文密钥扫描）。

### 5.3 合并规则（任务 06 实现为 `lsp.ResolveServers`）

对每个服务器 id，按顺序叠加，后者覆盖前者**非零**字段：

1. 目录条目（若 id 在目录中）；
2. 全局 `lsp.servers.<id>`；
3. 已同意的项目条目 `servers.<id>`。

然后决定 `enabled`：`<agentWorkspace>/state/lsp/enabled.json` 里有该 id 则以它为准；否则取叠加后的 `enabled`；仍为空则目录条目 false、自定义条目 true。

**配置指纹** = 合并结果（不含 `enabled`、`priority`、`prewarm`）的规范化 JSON 的 sha256 前 12 位十六进制。指纹变化的实例在下一次对账时关闭。

自定义条目（目录中没有的 id）必须有 `command` 和至少一个 `extension_to_language` 或 `filenames`，否则忽略并在 `/lsp` 里显示原因。

### 5.4 状态文件（每个 primary agent 一份，目录 `<agentWorkspace>/state/lsp/`）

| 文件 | 内容 |
|---|---|
| `enabled.json` | `{"enabled": {"gopls": true, "jdtls": false}}` |
| `recommendations.json` | `{"disabled": false, "disabled_reason": "", "dismissed_streak": 0, "never": ["gopls"]}` |
| `project_consent.json` | 见 §5.2 |
| `detect.json` | `{"<abs binary path>": {"mtime": 1727600000, "version": "v0.20.0"}}` |
| `pids.json` | Unix 孤儿进程清扫用，见 §7.8 |
| `logs/<id>-<roothash8>.log` | 实例日志，单文件超过 5 MiB 时轮转为 `.log.1`（只保留一份） |
| `cache/<id>/<roothash8>/` | jdtls `-data`、intelephense `storagePath` 等 |

`<roothash8>` = 根目录绝对路径 sha256 的前 8 位十六进制。所有写入用「写临时文件 + rename」原子替换。

### 5.5 Claude Code `.lsp.json` 映射（任务 16）

| Claude Code | forebrain | 转换 |
|---|---|---|
| 官方插件（13 个，映射表见任务 16） | 目录 id | 只写 `lsp.servers.<id>.enabled: true`，不复制字段 |
| 其他插件的服务器名 | `lsp.servers.<name>` | 字段映射如下 |
| `command` / `args` / `env` / `workspaceFolder` | 同名 snake_case | `${CLAUDE_PLUGIN_ROOT}` → 插件缓存目录绝对路径；含 `${CLAUDE_PROJECT_DIR}` 或 `${user_config.` 的条目跳过并报告 |
| `extensionToLanguage` / `initializationOptions` / `settings` | snake_case | 原样 |
| `startupTimeout` / `shutdownTimeout` | 秒 | 毫秒 ÷ 1000，向上取整 |
| `restartOnCrash` / `maxRestarts` / `diagnostics` | snake_case | 原样 |
| `transport` | — | 忽略 |

---

## 6. 内置语言服务器目录

### 6.1 条目格式

每个服务器一个文件 `pkg/lsp/catalog/<id>.yaml`，字段与任务 06 的 `CatalogEntry` 结构一一对应：

```yaml
id: gopls
display_name: gopls
languages: [Go]
role: primary
priority: 100
command: gopls
args: [serve]
extension_to_language: { .go: go }
filenames: { go.mod: go.mod, go.work: go.work }
root_markers: [go.work, go.mod]
root_strategy: nearest          # nearest | cargo-workspace
env_passthrough: [GOPATH, GOROOT, GOBIN, GOFLAGS, GOPROXY, GOPRIVATE, GONOPROXY, GONOSUMDB, GOTOOLCHAIN, GOWORK, CGO_ENABLED, CC, CXX, PKG_CONFIG_PATH, HTTP_PROXY, HTTPS_PROXY, NO_PROXY]
detect:
  extra_dirs: ["$GOBIN", "$GOPATH/bin", "~/go/bin"]
  version_args: [version]
install:
  - requires: go
    argv: [go, install, golang.org/x/tools/gopls@latest]
startup_timeout: 60
readiness: progress             # progress | rust-analyzer-status | jdtls-status | none
auto_answers: {}                # window/showMessageRequest：消息子串 → 选项标题
initialization_options: {}
settings: {}
conflicts: []                   # [{with: <id>, when_root_marker: [..]}]：满足时本条目胜出
project_writes: []              # 已知会写进项目目录的相对路径，doctor 与文档展示
notes: ""
```

安装配方用 **argv 数组**（不经 shell），`platforms` 可选（`[darwin, linux, windows]`，缺省全部）。命令在 darwin 上需要走 `xcrun` 的条目用 `command_darwin` / `args_darwin` 覆盖。

### 6.2 必选语言明细（任务 06 按此写 YAML）

| id | 命令 | 扩展名 → languageId | 根标记 | 安装 argv | 额外放行变量 | 特殊处理 |
|---|---|---|---|---|---|---|
| `gopls` | `gopls serve` | `.go→go`；文件名 `go.mod`、`go.work` | `go.work`、`go.mod` | `go install golang.org/x/tools/gopls@latest`（requires `go`） | 见 §6.1 示例 | 支持 workspaceFolders |
| `rust-analyzer` | `rust-analyzer` | `.rs→rust` | `Cargo.toml`、`rust-project.json`；`root_strategy: cargo-workspace` | `rustup component add rust-analyzer`（requires `rustup`） | `CARGO_HOME RUSTUP_HOME RUSTUP_TOOLCHAIN RUSTFLAGS HTTP_PROXY HTTPS_PROXY NO_PROXY` | `settings: {rust-analyzer: {cargo: {targetDir: true}}}`（不抢 agent 自己的 target 锁）；`readiness: rust-analyzer-status`；诊断多为迟到型 |
| `clangd` | `clangd --background-index --header-insertion=never --log=error` | `.c→c`、`.h→c`、`.cc .cpp .cxx .c++ .hpp .hh .hxx .ipp→cpp`、`.m→objective-c`、`.mm→objective-cpp`、`.cu .cuh→cuda-cpp` | `compile_commands.json`、`compile_flags.txt`、`.clangd`、`CMakeLists.txt`、`meson.build`、`Makefile` | darwin：`brew install llvm`（requires `brew`）；其他平台无自动配方（`notes` 提示用系统包管理器装 `clangd`） | `CPATH C_INCLUDE_PATH CPLUS_INCLUDE_PATH SDKROOT DEVELOPER_DIR` | macOS 探测先试 `xcrun --find clangd`；`project_writes: [.cache/clangd]`；没有 `compile_commands.json` 时 doctor 提示 `cmake -DCMAKE_EXPORT_COMPILE_COMMANDS=ON` 或 `bear -- make` |
| `jdtls` | `jdtls -data ${LSP_CACHE_DIR}` | `.java→java` | `pom.xml`、`build.gradle`、`build.gradle.kts`、`settings.gradle`、`settings.gradle.kts`、`mvnw`、`gradlew`、`.project` | darwin：`brew install jdtls`（requires `brew`） | `JAVA_HOME MAVEN_OPTS M2_HOME GRADLE_USER_HOME GRADLE_OPTS HTTP_PROXY HTTPS_PROXY NO_PROXY` | `settings: {java: {import: {generatesMetadataFilesAtProjectRoot: false}}}`；`startup_timeout: 180`；`readiness: jdtls-status`；需要 JDK 21+ 与 python3 |
| `kotlin-lsp` | `kotlin-lsp --stdio` | `.kt .kts→kotlin` | `settings.gradle.kts`、`settings.gradle`、`build.gradle.kts`、`build.gradle`、`pom.xml` | darwin：`brew install JetBrains/utils/kotlin-lsp` | `JAVA_HOME GRADLE_USER_HOME GRADLE_OPTS` | 必须 `--stdio`（默认是 socket）；`startup_timeout: 180` |
| `sourcekit-lsp` | darwin：`xcrun sourcekit-lsp`；其他：`sourcekit-lsp` | `.swift→swift` | `Package.swift`、`buildServer.json`、`compile_commands.json` | 无（随 Xcode / Swift 工具链） | `DEVELOPER_DIR SDKROOT TOOLCHAINS` | 支持 pull 诊断；`project_writes: [.build]` |
| `metals` | `metals` | `.scala .sc .sbt .mill→scala` | `build.sbt`、`build.sc`、`build.mill`、`project/build.properties`、`.bsp`、`project.scala` | `cs install metals`（requires `cs`） | `JAVA_HOME SBT_OPTS JAVA_OPTS COURSIER_CACHE HTTP_PROXY HTTPS_PROXY NO_PROXY` | `auto_answers: {"Import build": "Import build", "New sbt workspace detected": "Import build"}`；`initialization_options: {statusBarProvider: log-message, isHttpEnabled: false}`；`project_writes: [.metals, .bloop, .bsp]` |
| `csharp-ls` | `csharp-ls` | `.cs .csx→csharp` | `*.sln`、`*.slnx`、`*.csproj`、`global.json`、`Directory.Build.props` | `dotnet tool install --global csharp-ls`（requires `dotnet`） | `DOTNET_ROOT DOTNET_CLI_HOME NUGET_PACKAGES` | `detect.extra_dirs: ["~/.dotnet/tools"]`；未 `dotnet restore` 时 doctor 提示 |
| `typescript-language-server` | `typescript-language-server --stdio` | `.ts .mts .cts→typescript`、`.tsx→typescriptreact`、`.js .mjs .cjs→javascript`、`.jsx→javascriptreact` | `tsconfig.json`、`jsconfig.json`、`package.json` | `npm install -g typescript-language-server typescript`（requires `npm`） | `NODE_PATH NODE_OPTIONS NPM_CONFIG_PREFIX` | 优先项目内 `node_modules/typescript`（服务器自行探测） |
| `intelephense` | `intelephense --stdio` | `.php .phtml→php` | `composer.json` | `npm install -g intelephense`（requires `npm`） | — | `initialization_options: {storagePath: ${LSP_CACHE_DIR}, globalStoragePath: ${LSP_CACHE_DIR}/global}` |
| `pyright` | `pyright-langserver --stdio` | `.py .pyi→python` | `pyproject.toml`、`pyrightconfig.json`、`setup.py`、`setup.cfg`、`requirements.txt`、`Pipfile` | `npm install -g pyright`（requires `npm`） | `VIRTUAL_ENV CONDA_PREFIX PYTHONPATH` | `workspace/configuration` 的 `python` section 由客户端回填 `pythonPath`（§7.3） |

`${LSP_CACHE_DIR}` 是目录与配置里唯一由 LSP 层展开的占位符，展开为 `<agentWorkspace>/state/lsp/cache/<id>/<roothash8>`（启动前创建），可出现在 `args`、`initialization_options`、`settings` 的字符串值里。

### 6.3 同一文件多个服务器

- primary 选择顺序：项目条目 > 满足 `conflicts` 规则的条目 > `priority` 大者 > id 字典序小者。
- diagnostics 型与 primary 并行运行，诊断合并，每条带来源。

### 6.4 二进制探测（任务 06）

1. 用**服务器将得到的 PATH**（`SafeSubprocessEnv` 结果里的 PATH）做 `exec.LookPath`；再依次试 `detect.extra_dirs`（展开 `$VAR` 与 `~/`，变量为空的项跳过）。
2. darwin 且条目有 `command_darwin` 为 `xcrun` 时，用 `xcrun --find <tool>`（2 秒超时）判断是否安装。
3. 找到后跑 `<binary> <version_args>`（3 秒超时，取输出第一行，截断到 80 字符）；按「路径 + mtime」缓存到 `detect.json`。版本命令退出码非 0 视为「未安装」（rustup 代理在组件缺失时存在但报错）。
4. 探测**只在后台 goroutine 或 CLI 中执行**，不得在 `Runner.Load` 或任何界面首帧路径上执行。

### 6.5 安装（任务 12 实现 `Install`，任务 13、14 调用）

- 选择第一条 `platforms` 匹配当前 GOOS 且 `requires` 在 PATH 上的配方；没有则返回附录 C 的 `install-unavailable` 错误。
- 以用户身份在宿主执行 argv（`exec.CommandContext`，**完整继承 `os.Environ()`**，工作目录为用户 home），合并 stdout/stderr 逐行回调 `progress`，超时 10 分钟。
- 成功后清除该二进制的探测缓存并重新探测。
- 只能由用户的显式操作触发（推荐框、`/lsp` 面板、`forebrain lsp install`），模型不能触发。

---

## 7. `pkg/lsp` 运行时

### 7.1 传输与 JSON-RPC（任务 04）

- 报文头 `Content-Length: N`（忽略 `Content-Type` 与未知头，头名大小写不敏感），`\r\n\r\n` 后接 N 字节 JSON。单条消息上限 64 MiB，超限视为协议错误：关闭连接，由 supervisor 按崩溃处理。
- 请求 id 为递增 int64；待决表 `map[id]chan response`；ctx 取消时发 `$/cancelRequest`，立即返回 `ctx.Err()`，迟到的响应丢弃。
- 服务器→客户端请求在独立 goroutine 调用处理器，**必须应答**；未注册的方法回错误码 `-32601`。
- 写端单一互斥，保证同一文档的 `didOpen / didChange / didSave / didClose` 按调用顺序写出。
- stderr 写实例日志，不得写终端。

### 7.2 初始化（任务 05）

- `initialize` 参数：`processId = os.Getpid()`、`clientInfo {name: "forebrain", version: "dev"}`、`rootUri`、`rootPath`、`workspaceFolders [{uri, name: basename}]`、`initializationOptions`、`capabilities`（附录 A 原样）、`trace: "off"`、`locale: "en"`。
- 位置编码：客户端声明 `["utf-8", "utf-16"]`；服务器结果 `capabilities.positionEncoding` 为 `utf-8`、`utf-16`、`utf-32` 之一，缺省 `utf-16`。
- 收到结果后发 `initialized {}`；`settings` 非空时发 `workspace/didChangeConfiguration {settings}`。
- 能力判断：`ServerCapabilities` 以 `map[string]json.RawMessage` 保存；某能力「支持」当且仅当键存在且值不是 `null`/`false`。
- 整个初始化受 `startup_timeout` 约束，超时按启动失败处理。

### 7.3 服务器→客户端的请求与通知（任务 05）

| 方法 | 处理 |
|---|---|
| `workspace/configuration` | 对每个 item 的 `section`（点号路径）在合并后的 `settings` 中查找，找不到回 `null`；`section == "python"` 且结果里没有 `pythonPath` 时，按顺序探测 `$VIRTUAL_ENV/bin/python`、`<root>/.venv/bin/python`、`<root>/venv/bin/python`、PATH 上的 `python3`（Windows 用 `Scripts\python.exe` 与 `python.exe`）回填 |
| `client/registerCapability` / `unregisterCapability` | 记录注册；回 `null` |
| `window/workDoneProgress/create` | 回 `null`，开始跟踪 token |
| `$/progress` | `begin`/`report`/`end` 更新进度 |
| `window/showMessageRequest` | 消息包含 `auto_answers` 的某个键（子串，大小写不敏感）时回对应标题的 action；否则回 `null`；写日志 |
| `window/showMessage` / `window/logMessage` | 写日志；`type == 1`（Error）时记入 `last_error` |
| `window/showDocument` | 回 `{success: false}` |
| `workspace/applyEdit` | 回 `{applied: false, failureReason: "read-only client"}`，写日志 |
| `workspace/workspaceFolders` | 回实例当前 folders |
| `workspace/diagnostic/refresh` | 标记该实例需重新 pull；回 `null` |
| 其他 `*/refresh` 请求 | 回 `null` |
| `textDocument/publishDiagnostics` | 写入诊断存储 |
| `experimental/serverStatus` | `quiescent == true` → 就绪；`health == "error"` → `last_error = message` |
| `language/status` | `type == "ServiceReady"` → 就绪；`type == "Error"` → `last_error` |
| 其他通知 | 忽略 |

### 7.4 工作区根（任务 06，`lsp.ResolveRoot`）

1. 文件必须在项目根内：对两者 `filepath.EvalSymlinks` 后比较前缀；darwin 与 windows 上比较不区分大小写。否则不属于任何实例。
2. `workspace_folder` 非空：相对路径相对项目根解析；必须在项目根内；即为根。
3. 自文件所在目录向上逐级到项目根（含），第一个包含任一 `root_markers` 的目录为候选（marker 含 `*` 时用 `filepath.Glob`）。
4. `root_strategy: cargo-workspace`：从候选继续向上到项目根，若某级 `Cargo.toml` 中存在一行去掉空白后等于 `[workspace]`，取**最上层**这样的目录为根；否则用候选。
5. 没有候选：根 = 项目根。

### 7.5 实例与多根

- 实例键：`<serverID>\x00<配置指纹>\x00<根>`。
- 需要新根 R 时，若已有同服务器、同指纹、同项目根的实例，且其 `workspace.workspaceFolders.changeNotifications` 为 true 或非空字符串，则发 `workspace/didChangeWorkspaceFolders {event: {added: [R], removed: []}}` 并复用；否则新起实例。
- 超过 `max_servers`：关闭最久未使用的空闲实例；没有空闲实例则返回附录 C 的 `too-many-servers`。

### 7.6 文档同步（任务 07）

- 每个实例一张打开表：`uri → {version, sha256, mtime, size, languageId, lastUsed}`。
- `ensureSynced(path)`：未打开 → 读盘、`didOpen(version=1)`；已打开且 mtime 或 size 变化 → 读盘比哈希，变了则 `didChange`（全文替换，version+1）。
- agent 写入后额外发 `didSave`（服务器 `textDocumentSync.save.includeText` 为 true 时带全文）。
- 每实例最多 64 个打开文档，超出时 `didClose` 最久未用的。
- 大于 4 MiB、含 NUL 字节、或不是合法 UTF-8 的文件不打开。
- **何时打开**：`lsp` 工具调用与 agent 编辑会在需要时启动实例并打开文档；`DidRead` 只在实例**已在运行**时打开（作为基线），不得为读文件启动实例。

### 7.7 就绪与索引（任务 05）

- 状态：`starting → initializing → indexing → ready`，另有 `failed`、`stopped`。
- `readiness`：`progress`（`initialized` 后至少 500 毫秒内没有未结束的 work-done token）、`rust-analyzer-status`（收到 `quiescent: true`）、`jdtls-status`（收到 `ServiceReady`）、`none`（`initialized` 即就绪）。
- 索引中的工具请求：在 `request_timeout` 剩余时间内等就绪；等不到仍发请求，结果首行加附录 B.6 的索引提示。

### 7.8 生命周期与进程树（任务 05）

- 启动：`exec.Cmd` 的 `Dir` = 根；`Env` 按 §9.3 构建；stdin/stdout 接 JSON-RPC；stderr 接日志。
- Unix：`SysProcAttr.Setpgid = true`。关闭：`shutdown` 请求（`shutdown_timeout`）→ `exit` 通知 → 等 1 秒 → `kill(-pgid, SIGTERM)` → 等 2 秒 → `kill(-pgid, SIGKILL)` → `Wait()`。
- Windows：启动后立刻把进程加入一个设置了 `JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE` 的 Job Object；关闭时 `TerminateJobObject` 并关闭句柄。
- forebrain 自身被强杀时的孤儿（所有 Unix）：启动时把 `{pid, pgid, started}` 写入 `<agentWorkspace>/state/lsp/pids.json`（`started` 为 `ps -o lstart= -p <pid>` 的输出），正常退出时删除；下一次创建 Manager 时，对记录里仍存在、且 `started` 与记录一致的进程组发 SIGKILL，然后删除这些记录。另外，服务器收到的 `processId` 让遵守规范的服务器在父进程消失后自行退出。
- 崩溃：进程意外退出 → 状态 `failed`；`restart_on_crash` 为 true 且 10 分钟内重启次数 < `max_restarts` 时，按 1、2、4 秒退避重启，重新初始化并按打开表重新打开文档；进行中的请求返回错误「server restarted」。超过次数后停在 `failed`，`last_error` 写「crashed N times in 10 minutes」，只有 `/lsp` 重启或配置指纹变化才会再启动。
- 空闲回收：Pool 每 60 秒检查一次，实例无在途请求且 `lastUsed` 早于 `idle_timeout` 即优雅关闭。

---

## 8. 面向模型的能力

### 8.1 `lsp` 工具（任务 09 实现查询，任务 10 实现工具）

参数（Go 结构体见任务 10，字段描述逐字见附录 B.2）：`operation`（必填枚举）、`file_path`、`line`（1-based）、`column`（1-based，按字符）、`symbol`、`query`、`include_declaration`、`max_results`（默认 50，上限 200）。

| operation | LSP 方法 | 必需参数 |
|---|---|---|
| `definition` / `declaration` / `type_definition` / `implementation` | `textDocument/definition` / `declaration` / `typeDefinition` / `implementation` | file_path + line + (column 或 symbol) |
| `references` | `textDocument/references`（`context.includeDeclaration` = `include_declaration`） | 同上 |
| `hover` | `textDocument/hover` | 同上 |
| `document_symbols` | `textDocument/documentSymbol` | file_path |
| `workspace_symbols` | `workspace/symbol`（结果缺 range 时 `workspaceSymbol/resolve`） | query；可选 file_path 用于选服务器 |
| `incoming_calls` / `outgoing_calls` | `textDocument/prepareCallHierarchy` + `callHierarchy/incomingCalls` / `outgoingCalls`（对 prepare 返回的第一个 item） | 同 definition |
| `supertypes` / `subtypes` | `textDocument/prepareTypeHierarchy` + `typeHierarchy/supertypes` / `subtypes` | 同 definition |
| `diagnostics` | 有 file_path：pull（支持时）否则存储里的推送结果；无 file_path：所有运行中实例已打开文档的诊断汇总 | — |

`workspace_symbols` 没有 file_path 时使用项目内**已在运行**的 primary 实例（按 id 字典序第一个）；一个都没有时返回附录 C 的 `no-running-server`。

### 8.2 位置与符号（任务 04 实现 `position.go`）

**输入换算**（1-based 行列 → LSP Position）：

1. 使用与服务器同步的那份内容；按 `\n` 切行，行尾 `\r` 不计入。
2. 行号超出 → 附录 C 的 `line-out-of-range`。
3. 列按 Unicode 码点计；超出「码点数 + 1」时夹到行尾，结果首行注明 `(column clamped to end of line)`。
4. `character` = 该行前 `column-1` 个码点在协商编码下的长度：utf-8 为字节数；utf-16 中 U+10000 及以上计 2、其余计 1；utf-32 为码点数。

**输出换算**（LSP Position → 1-based 行列）是上述的逆运算；目标文件不可读（根外、超过 4 MiB）时直接输出 `line+1:character+1`，不换算。

**symbol 锚定**：

1. `symbol` 去首尾空白；若含 `.`、`::`、`->`、`#`，取最后一段。
2. 在第 `line` 行查找，要求前后相邻字符都不是标识符字符（Unicode 字母、数字、`_`、`$`），取第一个出现，列 = 其首码点位置 + 1。
3. 找不到依次查 `line-1, line+1, line-2, line+2, line-3, line+3`；找到则结果首行注明 `(symbol found on line X)`。
4. 仍找不到 → 附录 C 的 `symbol-not-found`。同时给了 `column` 和 `symbol` 时以 `symbol` 为准。

### 8.3 编辑后诊断与等待窗口（任务 08 实现 `DidWrite`，任务 11 接入工具）

#### 8.3.1 调用方

`write_file`、`edit_file`、`apply_patch` 写盘成功后，若 `rt.CodeIntel != nil` 且 `lsp.diagnostics.after_edit` 为 true，调用 `DidWrite(ctx, llm.AgentSessionIDFromContext(ctx), changes)`；一次 `apply_patch` 的所有文件放在**同一次**调用里。会话 id 由调用方传入，因为 `pkg/lsp` 不能导入 `pkg/llm`（§3.3）。

#### 8.3.2 等待窗口的定义

- **起点**：`DidWrite` 被调用的时刻。服务器启动、取基线、同步、收集都计入同一个窗口；**一次调用只有一个窗口**，多个文件共享，不是每个文件各一个。
- **上限**：`lsp.diagnostics.wait_ms`（默认 2500 毫秒）。
- **提前结束**（满足即返回，不等满）：每个受影响的实例都已拿到本次编辑后的诊断——pull 型：`textDocument/diagnostic` 返回；push 型：收到目标文件 version ≥ 本次发送 version 的 `publishDiagnostics`（服务器不支持 versionSupport 时，收到发送之后的第一次推送即可），并且之后经过 300 毫秒安静期。
- **零开销**：没有已启用服务器负责的文件不参与，所有文件都无人负责时 `DidWrite` 立即返回空结果。
- **等满不算失败**：窗口到期时，已收到的诊断照常报告；没收到的文件在结果末尾写附录 B.3 的 pending 行，并登记为迟到诊断（§8.4）。工具本身永远返回成功。
- **取消**：工具调用的 ctx 被取消（用户按 Esc）时立即停止等待并返回；已写盘的内容不回滚，诊断转为迟到。
- **`wait_ms: 0`**：不等待，全部诊断走迟到提醒。

时间线示例：

| 服务器 | 过程 | 编辑工具多花的时间 |
|---|---|---|
| pull 型（如 sourcekit-lsp） | 同步后发 `textDocument/diagnostic`，400 ms 返回 | ≈0.4 s |
| push 型（如 gopls） | 600 ms 收到目标文件诊断，再安静 300 ms（期间测试文件的推送也收进来） | ≈0.9–1 s |
| 要跑 `cargo check`（rust-analyzer） | 2.5 s 到期先返回；8 s 时诊断到达，登记为迟到 | 2.5 s，诊断晚一轮送达 |
| 服务器尚未启动（jdtls 冷启动） | 2.5 s 内没完成启动，返回 pending 行 | 2.5 s |

与其他时间参数的区别：

| 参数 | 作用于 | 到期后果 |
|---|---|---|
| `lsp.diagnostics.wait_ms` | 编辑工具等诊断 | **不是错误**，剩余诊断迟到提醒 |
| `startup_timeout` | 服务器启动与初始化 | 启动失败，按崩溃规则处理 |
| `lsp.request_timeout` | 一次 `lsp` 工具调用 | 工具返回附录 C 的 `request-timeout` 错误 |
| `shutdown_timeout` | 优雅关闭 | 强制杀进程树 |

#### 8.3.3 基线

- 文件在调用前已被实例打开：基线 = 存储里该文件当前的诊断。
- 未打开：用 `Before` 内容 `didOpen`，在「窗口的一半」内取诊断作为基线，再 `didChange(After)`。
- 取不到基线（服务器在索引、超时）：标记 `baseline_unavailable`，报告编辑后的**全部**问题，用附录 B.3 的对应首行。
- 新建文件（`Before == nil`）：基线为空。删除文件（`After == nil`）：发 `didClose`，不报告该文件。

#### 8.3.4 「新引入」的计算

键 = `severity` + `\x1f` + `source` + `\x1f` + `code` + `\x1f` + message（去首尾空白、连续空白压成一个空格）。对每个键，报告 `max(0, 编辑后数量 − 基线数量)` 条（取编辑后列表中该键靠后的条目）。**不用行号做键**，编辑造成的行号偏移不会让旧问题被当成新问题。

其他文件：窗口内推送了诊断且在项目根内的其他文件，同样按键计算增量（基线 = 本次调用开始时存储里的诊断）。

过滤与排序：丢弃低于 `min_severity` 的；按 severity（error、warning、information、hint）→ 本次编辑的文件在前、其他文件按路径字典序 → 行 → 列排序；每文件最多 `max_per_file` 条、最多 `max_files` 个文件，超出部分用附录 B.3 的「more not shown」行说明。

#### 8.3.5 输出

- 有新问题：工具返回文本 = 原成功文本 + `"\n\n"` + 附录 B.3 的 `<diagnostics>` 块；`apply_patch` 追加在 `stdout` 字段末尾。
- 没有新问题且没有 pending：**不追加任何文字**。
- `CaptureToolOutput` 增加 `"lsp_diagnostics": event.LSPDiagnosticsSummary`，界面据此显示 `Found N new diagnostic issues in M files`。

### 8.4 迟到诊断（任务 11）

- Manager 按 agent 会话（`llm.AgentSessionIDFromContext`）记录「本会话编辑过的文件」，保留 30 分钟。这些文件在等待窗口之后收到的诊断，按 §8.3.4 与「该会话上次报告给模型的诊断」计算增量，非空则加入该会话的待办。
- 新 wrapper `wrapLSPDiagnosticsReminderLLM`（写在 `pkg/run/config.go`，与提醒工具函数放在一起）挂在 `runner.go` 的 `wrapSkillOfferLLM` 之后（仍在 `summaryChain` 之外）：每次模型请求前 `PeekLate(sessionID)`，有内容则把附录 B.4 的文本经 `planReminderMessage` 包装后追加到消息末尾，`recordReminderAdoption(ctx, msg, len(msgs))`，请求成功后 `AckLate(sessionID, token)`；请求失败不 Ack，下次再发。
- `lsp.diagnostics.late_delivery: false` 时不登记迟到诊断。
- 子代理与 fork 各有自己的 agent 会话 id，互不串扰。

### 8.5 工具注册与 prompt 缓存（任务 03、10）

- `process` 在构建 `run.Deps` 时调用 `lsp.ToolEnabled(...)` 计算 `Deps.CodeIntelTool`：`features.lsp && 项目受信任 && 合并后的服务器集合里至少一个已启用的 primary 服务器`。主 Runner 与每个项目 Runner 各算一次，之后不再改变（配置重载不重算）。任务 03 接好调用点（骨架实现恒为 false），任务 06 实现真实判断。
- `tool.RegisterDefaultTools` 在 `rt.CodeIntel != nil && rt.CodeIntelTool` 时注册 `lsp`，位置固定在 `request_permissions` 之后、`registerSessionAndMemoryTools` 之前。
- 描述与 schema 是常量；不改系统提示。
- `ToolMeta{ReadOnly: true, ConcurrencySafe: true}`，category `"code"`。

### 8.6 可见性

- 计划模式：可用（只读）。
- 类型化子代理：不加入 `typedSubagentDisallowedTools`，全部可见。
- 子代理与 fork：`Factory.NewIsolatedRunner` 必须继承 `CodeIntel`、`CodeIntelControl`、`CodeIntelTool`。
- guardian：不用工具。

---

## 9. 权限与安全

### 9.1 威胁模型：打开项目 ≈ 构建项目

rust-analyzer 执行 `build.rs` 与过程宏；jdtls、kotlin-lsp、metals 执行构建脚本并联网；gopls 在 `GOTOOLCHAIN=auto` 时可能下载并运行工具链；typescript-language-server 加载 tsconfig 插件；项目级 `settings` 可让服务器运行任意程序（如 rust-analyzer 的 `check.overrideCommand`）。风险与 MCP stdio 服务器同级。

### 9.2 启动闸门（全部满足才启动）

1. `features.lsp` 为 true。
2. 项目根非空且受信任（`safety.TrustedRoot(launch) != ""`）。
3. 服务器已启用（§5.3）。
4. 若服务器配置用到了项目条目：该条目已同意。
5. 二进制已找到。

任一不满足：不启动；工具调用返回附录 C 的对应错误。

### 9.3 环境变量

`home.SafeSubprocessEnv(nil, home.Options{ExplicitEnv: m})`，其中 `m` = 目录与全局条目 `env_passthrough` 列出的、在父进程中存在的变量 + 展开后的 `env`（全局条目从进程环境展开 `${VAR}`；项目条目只从 `~/.forebrain/.env` 展开，与 `pkg/mcp/stdio_secure.go` 的 `envLookupForServer` 一致）。像密钥的名字仍被过滤。

### 9.4 工具权限（任务 10）

- `pkg/run/permissions.go`：`permissionToolName("lsp")` 在参数含非空 `file_path` 时返回 `"Read"`，否则返回 `"LSP"`；`permissionInput("lsp")` 返回 `file_path`。效果：`Read(...)` 的 allow/deny/ask 规则同样约束 LSP。
- `pkg/safety`：`canonicalToolAliases` 加 `"lsp": "LSP"`；`safeReadOnlyTools` 加 `"LSP"`（注释写明：只读、带路径的调用已按 Read 评估）。
- 工具在联系服务器之前，对 `file_path` 做与 `read_file` 相同的授权（抽出的 `authorizeRead`）：`resolveReadFilePath`、`ProtectedReadReason`（走审批）、`ReadPathDenied`。
- 结果里可读根之外的位置只给路径和行列，不给预览（附录 B.5）。

### 9.5 只读保证

见 §1.3 与 §7.3。

### 9.6 沙箱

本规范范围内服务器在宿主运行（与 MCP stdio 相同），由 §9.2 把关，`/lsp` 与文档明确说明。长驻进程沙箱列入 P3（§12.2）。

---

## 10. 推荐（截图对应的功能，任务 13）

### 10.1 触发（全部满足）

1. `features.lsp && lsp.recommendations`，且 `recommendations.json` 的 `disabled` 为 false。
2. 项目受信任。
3. 触发点：`DidWrite` 中某个被写文件没有已启用服务器负责，且其扩展名或文件名命中某个**未启用**、不在 `never` 列表里的目录服务器（多个命中时按 §6.3 选 primary）。
4. 调用上下文不是 fork 子会话（`tool.IsForkChildFromContext`）也不是类型化子代理（`tool.SubagentTypeFromContext != ""`）。
5. 本会话（`tool.ConversationSessionIDFromContext`）还没推荐过任何服务器。
6. 二进制已找到 → `mode: "enable"`；未找到但有可用安装配方 → `mode: "install"`；都没有 → 不推荐。

推荐以回调 `SetRecommendationListener` 交出。监听器由组合根 `pkg/process` 为主 Runner 与每个项目 Runner 各安装一次（子代理与 fork 共用父 Runner 的 Manager，若在 `pkg/run` 的载入路径上安装，子 Runner 会覆盖父 Runner 的监听器），发布时读取 `runner.Events`，发出 `RunEvent{Type: "lsp_recommendation", SessionID: 会话 id, RunID, Payload: event.LSPRecommendation}`（任务 13）。二进制探测在后台 goroutine 进行，不阻塞 `DidWrite`（探测未完成时本次不推荐，下一次编辑再判断）。

### 10.2 选项与持久化

| 选项（TUI 文案） | choice | 效果 | 持久化 |
|---|---|---|---|
| `Yes, enable` | `enable` | 启用；后台启动该服务器；下一次编辑起有诊断 | `enabled.json`；`dismissed_streak = 0` |
| `Yes, install and enable` | `install` | 执行安装（§6.5），成功后同上；失败显示输出尾部 | 成功时同上 |
| `No, not now`（Esc 同） | `not_now` | 本会话不再推荐 | `dismissed_streak + 1`；达到 5 时 `disabled = true`、`disabled_reason = "dismissed 5 times in a row"` |
| `Never for <id>` | `never` | 此服务器永不推荐 | `never` 加入 id；`dismissed_streak = 0` |
| `Disable all LSP recommendations` | `disable_all` | 不再推荐任何服务器 | `disabled = true`、`disabled_reason = "turned off by the user"` |

**与 Claude Code 的差异（有意为之）**：不做 30 秒自动关闭。终端的模态选择器读取按键是阻塞读，没有超时能力（`pkg/tui/overlays.go:3235 readKeyWithMode`）；为此改造输入层风险大于收益。改为「连续 5 次『暂不』自动关闭」，同样避免反复打扰。

重新开启：`/lsp` 面板的「Reset recommendations」或 `forebrain lsp recommendations reset`，清空 `disabled`、`disabled_reason`、`dismissed_streak`、`never`。

### 10.3 界面

- **终端**：`ChatSession.publishRunEvent` 收到 `lsp_recommendation` → `notifyUI(LSPRecommendationMsg{...})` → `processUINotification` 里像 `MigrationPreviewMsg` 一样调用 `selector.Review(label, facts, actions, 0)`，再调用 `DecideRecommendation`。模态框在回合进行中也可出现，不暂停 agent。
- **Web**：`useChatStream.ts` 收到非历史的 `lsp_recommendation` → 聊天区显示 `LspRecommendationCard.vue`（按钮对应各选项 + 关闭按钮，关闭 = `not_now`）→ `POST /api/v1/lsp/recommendations/:id/decision`。历史回放不显示。
- 决定之后在界面 transcript 留一行提示（如 `gopls enabled for Go`），不进入模型上下文。

终端 facts（`turn.StatusFact`）：

| Label | Value |
|---|---|
| `Server` | `gopls (Go)` |
| `Found` | `~/go/bin/gopls (v0.20.0)`；install 模式为 `Not installed. Install with: go install golang.org/x/tools/gopls@latest` |
| `Triggered by` | `.go files` |
| `Runs` | `in this trusted project, outside the sandbox` |

label（`selector.Review` 的首行是标题，其后各行是副标题）：`LSP recommendation\nA language server gives the agent diagnostics after its edits and lets it find definitions and references by symbol. Enable this language server?`

actions：enable 模式 `Yes, enable` / `No, not now` / `Never for <id>` / `Disable all LSP recommendations`，默认选中第一项；install 模式第一项为 `Yes, install and enable`，**默认选中 `No, not now`**（执行安装命令不能只差一次误按回车）。

---

## 11. 界面、命令与迁移

### 11.1 `/lsp`（任务 14）

- `pkg/turn/slash.go:73` 的命令表加 `{Name: "lsp", Description: "language servers: status, enable, restart, diagnostics", AllowedSurfaces: []Surface{SurfaceWebChat, SurfaceTUI}, SupportsInlineArgs: false, Visibility: VisibilityPublic}`，并在同文件与 `mcp` 同列的分支里加入 `lsp`。
- 文本渲染放在 `pkg/turn/mcp_format.go`（`turn` 已满 20 个文件）：`RenderLSPInventoryMarkdown(event.LSPSnapshot) string`，两个界面的纯文本回退共用。
- 终端面板：`uiPanel{kind: "lsp"}`，照 `openMCPPanel` 实现；行格式与按键见任务 14。
- Web：`frontend/src/views/LspView.vue`（照 `McpView.vue`），路由 `/lsp`，导航项 `nav.lsp`。

### 11.2 工具卡片

- `pkg/tool/format.go`：`FormatToolStepResult` 增加 `case "lsp": formatLSPStep`；`formatGenericToolStep` 在 `turnDiffSection` 之后追加 `lspDiagnosticsSection`（正文以 `lsp diagnostics: ` 开头）。
- 终端：`pkg/tui/render.go` 的工具标题分支给 `lsp` 用动词（`Looking up` / `Looked up` + 操作 + 目标）；`failedActionPhrase("lsp") = "look up"`；编辑卡片识别 `lsp diagnostics:` 段落，显示 `└ Found N new diagnostic issues in M files`。
- Web：`ToolCallCard.vue` 无需改结构，正文来自同一个 `FormatToolStepResult`。

### 11.3 `/status`

`turn.StatusReport` 增加一行 `Language servers`：`2 running, 1 indexing, 1 failed`；没有任何已启用服务器时不显示。

### 11.4 命令行（任务 12）

`forebrain lsp list | doctor [--server <id>] [--project <dir>] [--no-handshake] | enable <id> | disable <id> | install <id> [--yes] | recommendations reset`。前五个由任务 12 实现（输出格式见该任务），`recommendations reset` 由任务 13 实现（它依赖推荐状态文件）。

### 11.5 `/migrate`（任务 16）

新增迁移类别 `lsp`（与 `mcp` 等并列）。Claude 源：`enabledPlugins` 里的插件，官方 13 个按任务 16 的映射表写 `lsp.servers.<id>.enabled: true`；其他插件读 `.lsp.json` 与 `plugin.json` 的 `lspServers` 写成自定义条目。写入 `forebrain.yaml`，已存在同名条目时跳过并报告。Codex 源无 LSP。

---

## 12. 交付计划

### 12.1 任务计划（P0–P2，全部可直接交给模型执行）

详见 [`docs/plan/lsp/README.md`](lsp/README.md)。

| # | 任务 | 依赖 |
|---|---|---|
| 01 | `lsp` 配置段与 `features.lsp` | — |
| 02 | port 接口与 DTO | — |
| 03 | `pkg/lsp` 骨架与组合根接线（process / run / factory / runner pool / 分层表） | 01、02 |
| 04 | 协议层：JSON-RPC、协议类型、位置换算、URI | 03 |
| 05 | 服务器实例：进程、环境、初始化、服务器请求、就绪、重启、进程树、假服务器 | 04 |
| 06 | 目录（12 门语言）、配置合并、根解析、二进制探测、`ToolEnabled` | 03 |
| 07 | 文档同步与诊断存储 | 05 |
| 08 | 实例池与 Manager 的诊断侧和控制面 | 06、07 |
| 09 | 查询操作与结果格式化 | 08 |
| 10 | `lsp` 工具与权限 | 09 |
| 11 | 编辑后诊断、迟到提醒、读观察者、shell 变更扫描 | 08 |
| 12 | `forebrain lsp` 命令、安装、集成测试与 CI | 09 |
| 13 | 推荐：引擎、终端模态框、Web 卡片 | 11、12、14 |
| 14 | `/lsp` 面板、Web 页面、REST、`/status` | 12 |
| 15 | 项目级 `lsp_servers.yaml` 与同意 | 08 |
| 16 | `/migrate` 导入 | 01 |
| 17 | 扩展语言与诊断型服务器 | 06 |
| 18 | 文档 | 全部 |

### 12.2 P3（不在本批次；开始前另写计划）

- 长驻进程沙箱：`safety.Manager` 新增返回包装后 `*exec.Cmd` 的 API（bubblewrap / seatbelt），LSP 配置档（读：项目 + 工具链 + 缓存；写：缓存 + 已知构建输出目录；网络按服务器）。
- `rename_preview` / `code_actions` 预览操作（WorkspaceEdit → 统一 diff，落盘仍走 `apply_patch`）。
- `fsnotify` 监听清单文件；全项目诊断操作（`workspace/diagnostic`）。
- C# Roslyn LS（需 `solution/open` 自定义通知）；Swift 的 Xcode 工程引导（xcode-build-server）。

---

## 13. 测试总则

每个任务计划列出自己的测试；以下是贯穿全部任务的要求：

- 纯逻辑（分帧、位置、符号、根、增量、合并、格式化）用表驱动单元测试，放在对应文件旁的 `_test.go`。
- 进程相关用假服务器（任务 05 写在 `pkg/lsp/instance_test.go` 的源码字符串常量 `fakeServerSource`，测试时写到临时目录并 `go build`，照 `pkg/mcp/bridge_test.go:398 buildProcessFixture` 与 `:447 processFixtureSource`；**不得**放进 `testdata/*.go`，见 §3.14）；**单元测试不得依赖真实语言服务器**。
- 真实服务器只在环境变量 `FOREBRAIN_LSP_INTEGRATION` 非空时运行：测试写在 `pkg/lsp/manager_test.go`（§3.14 的命名规则不允许单独的集成测试文件，也不能给已有测试文件加 build tag），由 `.github/workflows/lsp-integration.yml` 执行（任务 12）。
- 模型可见文本（附录 B）有 golden 测试。
- 进程泄漏测试照 `pkg/mcp/bridge_test.go` 的 `waitForProcessGone`。

---

## 14. 风险与对策

| 风险 | 对策 |
|---|---|
| 服务器吃内存 / CPU | 懒启动、`max_servers`、`idle_timeout`；doctor 显示常驻内存 |
| 首次索引慢 | 迟到诊断提醒；结果标注 indexing；`prewarm` 可选 |
| 每次编辑多等 | 等待窗口是上限、pull 优先、无服务器零开销、可调小或设 0 |
| 服务器往项目目录写东西 | 目录默认设置重定向（jdtls metadata、rust-analyzer targetDir）；`project_writes` 进 doctor 与文档 |
| 执行项目代码 | §9.2 闸门 |
| 诊断误报 | 只报新引入；doctor 提示；每服务器可关 `diagnostics` |
| 与 agent 自己的构建争锁 | rust-analyzer 独立 targetDir；jdtls 独立 `-data` |
| 服务器行为不标准 | 能力驱动、`auto_answers`、假服务器矩阵、集成测试 |
| 孤儿进程 | 进程组 / Job Object / pids.json 清扫；`processId` 让服务器自退 |
| Windows 路径与 URI | `uri.go` 规范化 + Windows 测试 |

---

## 15. 已定稿的决策

| # | 问题 | 定稿 | 被否决的备选及理由 |
|---|---|---|---|
| 1 | 目录服务器默认启用吗？ | **否**，用户显式启用一次（推荐框一键） | 检测到二进制即自动启用：会在受信任项目里自动执行构建脚本 |
| 2 | 推荐何时触发？ | **编辑时**（与 Claude Code 一致） | 读取时也触发：只是看代码也会被打扰 |
| 3 | `lsp` 工具何时注册？ | **受信任项目且冻结时至少一个已启用 primary 服务器** | 总是注册：每个会话多一段固定前缀，无服务器项目里模型会徒劳调用 |
| 4 | forebrain 可以执行安装命令吗？ | **可以**，只在用户在界面上看到完整命令并确认后执行；argv 不经 shell | 只显示命令：失去一步到位的体验 |
| 5 | 本批次是否在沙箱外运行？ | **是**，与 MCP stdio 同级，由 §9.2 把关；沙箱列入 P3 | 先做沙箱：工期显著变长，jdtls / metals 的网络与缓存路径需先摸清 |
| 6 | 协议类型自己写还是用第三方？ | **自己写 3.17 子集** | 第三方包版本老、联合类型处理不全、依赖大 |
| 7 | 诊断怎么交给模型？ | **等待窗口内附在工具结果，之后的走提醒** | 只走提醒：模型要晚一轮才看到，且与编辑分离 |
| 8 | 推荐的自动关闭 | **连续 5 次「暂不」** | Claude Code 的 30 秒超时：终端选择器没有超时读取能力（§10.2） |
| 9 | LSP 的权限评估 | **带 `file_path` 按 `Read` 评估，否则按只读的 `LSP`** | 独立的 `LSP(path)` 规则：多一套规则语法，且与 Read 规则可能矛盾 |
| 10 | 启用状态存哪 | **`state/lsp/enabled.json` 覆盖层，不改写 YAML** | 改写 `forebrain.yaml`：丢注释，与 MCP 禁用存储的做法不一致 |

---

## 附录 A：`initialize` 的 ClientCapabilities（逐字）

任务 05 把它作为 `pkg/lsp/instance.go` 里的字符串常量 `clientCapabilitiesJSON` 原样发送（不另建文件，§3.14）。

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

## 附录 B：模型可见文本（逐字）

### B.1 工具名与描述

- 名称：`lsp`
- 描述（Go 常量 `lspToolDescription`）：

```
Look up code through the project's language servers: definitions, declarations, type definitions, implementations, references, hover type information, document and workspace symbols, call and type hierarchies, and current diagnostics. Read-only. Lines are 1-based and numbered the way file reads show them. Pass symbol (a name on that line) instead of column when you are not sure of the exact column. Prefer this over text search when you need where a symbol is defined or used.
```

### B.2 参数描述（`jsonschema_description`）

| 字段 | 描述 |
|---|---|
| `operation` | `What to look up.` |
| `file_path` | `Absolute or workspace-relative file path. Required for every operation except workspace_symbols.` |
| `line` | `1-based line number, as shown by file reads.` |
| `column` | `1-based column in characters. Omit when symbol is given.` |
| `symbol` | `A name on the given line to position on, used instead of column.` |
| `query` | `Symbol name or prefix to search for. Required for workspace_symbols.` |
| `include_declaration` | `For references: also return the declaration itself.` |
| `max_results` | `Maximum locations to return (default 50, at most 200).` |

### B.3 编辑后诊断块

```
<diagnostics>
{N} new problem{s} after this edit ({server ids, comma separated})
{relative path}
  {severity} {line}:{column} {message} [{source} {code}]
</diagnostics>
```

规则：

- `{s}`：N 为 1 时为空，否则为 `s`。
- `{severity}` 为 `error`、`warning`、`info`、`hint` 之一；`[{source} {code}]` 中为空的部分省略，两者都空时整个方括号省略；message 中的换行替换为 ` / `。
- 路径相对项目根，用 `/` 分隔。
- 某文件超出 `max_per_file`：该文件末尾加 `  … {K} more in this file not shown`；文件数超出 `max_files`：块末尾加 `… {K} more files not shown`。
- 基线取不到时，首行改为：`{N} problem{s} reported after this edit ({server ids}); earlier problems could not be told apart`。
- 有未等到诊断的文件时，块末尾（`</diagnostics>` 之前）每个文件一行：`diagnostics for {relative path} are still being computed and will follow`。只有 pending、没有新问题时，块只含 pending 行（首行省略）。

### B.4 迟到诊断提醒

```
Language server diagnostics changed for files you edited earlier in this session:
{relative path}
  {severity} {line}:{column} {message} [{source} {code}]
```

同一文件的问题全部消失时写：`{relative path}: no remaining problems`。整段经 `planReminderMessage` 包成 `<system-reminder>`。

### B.5 查询结果

- 位置列表（definition / declaration / type_definition / implementation / references）：

```
{operation label} `{target}`: {N} result{s} in {M} file{s}
{relative path}
  {line}:{column}  {trimmed source line, at most 160 characters}
```

  `{operation label}`：`definition of`、`declaration of`、`type definition of`、`implementations of`、`references to`；`{target}` 为 symbol，或 `{line}:{column}`。可读根之外的路径写绝对路径并在行尾加 `  [outside project]`，不给源码行。结果被截断时末尾加 `… {K} more not shown (raise max_results, at most 200)`。空结果：`no results for {operation} at {relative path}:{line}:{column}`。
- `hover`：首行 `hover at {relative path}:{line}:{column}`，其后为服务器返回的内容（markdown 原样），超过 2000 字符截断并加 `…`。空结果：`no hover information at {relative path}:{line}:{column}`。
- `document_symbols`：首行 `symbols in {relative path}`，其后每个符号一行 `{indent}{kind} {name}  {startLine}-{endLine}`（缩进每层两空格，kind 为 LSP SymbolKind 的小写英文名，如 `function`、`method`、`struct`）。
- `workspace_symbols`：首行 ``workspace symbols matching `{query}`: {N}``，其后 `{kind} {name}  {relative path}:{line}`。
- `incoming_calls` / `outgoing_calls`：首行 ``callers of `{name}`: {N}`` / ``calls made by `{name}`: {N}``，其后 `{name}  {relative path}:{line}`。
- `supertypes` / `subtypes`：首行 ``supertypes of `{name}`: {N}`` / ``subtypes of `{name}`: {N}``，同上格式。
- `diagnostics`：首行 `diagnostics for {relative path}: {N}`（无 file_path 时 `diagnostics in open files: {N}`），其后同 B.3 的文件与问题行格式。

### B.6 提示行（出现在结果首行，可多行叠加，顺序固定：索引 → 列 → 符号）

- `language server {id} is still indexing; results may be incomplete`
- `(column clamped to end of line)`
- `(symbol found on line {X})`

## 附录 C：错误消息（逐字，工具以 error 返回）

| 键 | 文本 |
|---|---|
| `no-server` | `no language server handles {ext} files in this project; the user can enable one with /lsp` |
| `not-installed` | `language server {id} is enabled but its command "{command}" was not found; install it with: {install command}`（无配方时去掉 `; install it with: …`） |
| `outside-project` | `{path} is outside the project, so no language server covers it` |
| `untrusted` | `language servers only run in trusted projects` |
| `unsupported` | `language server {id} does not support {operation}` |
| `start-failed` | `language server {id} failed to start: {reason}` |
| `too-many-servers` | `too many language servers are running ({n}); raise lsp.max_servers or stop one with /lsp` |
| `missing-args` | `operation {operation} requires file_path, line, and column or symbol` |
| `missing-query` | `workspace_symbols requires query` |
| `request-timeout` | `language server {id} did not answer {operation} within {n}s` |
| `file-too-large` | `{path} is larger than 4 MiB; language servers are not given files that large` |
| `line-out-of-range` | `line {line} is past the end of {path} ({n} lines)` |
| `symbol-not-found` | `symbol "{symbol}" not found on line {line} of {path} (searched lines {from}-{to})` |
| `no-running-server` | `no language server is running in this project yet; pass file_path to start the one for that file` |
| `install-unavailable` | `no install recipe for {id} on this system; install it manually: {notes}` |
| `server-error` | `language server {id} could not answer {operation}: {message}`（服务器以 JSON-RPC 错误回应时） |
| `unknown-operation` | `unknown operation "{operation}"` |

## 附录 D：配置示例

```yaml
# ~/.forebrain/forebrain.yaml
features:
  lsp: true
lsp:
  diagnostics:
    wait_ms: 2500
    min_severity: warning
  servers:
    rust-analyzer:
      settings:
        rust-analyzer:
          cargo: { targetDir: true, features: all }
    jdtls:
      args: ["-data", "${LSP_CACHE_DIR}", "--jvm-arg=-Xmx3G"]
```

```yaml
# <project>/.forebrain/lsp_servers.yaml —— 受信任 + 受版本控制 + 逐条同意
servers:
  typescript-language-server:
    initialization_options:
      tsserver: { path: "node_modules/typescript/lib" }
```

## 附录 E：文件变更总表

`pkg/lsp` 共 17 个生产文件（上限 20）：`doc.go`、`pool.go`、`manager.go`、`control.go`（任务 03 建骨架，任务 08 实现）、`jsonrpc.go`、`protocol.go`、`position.go`（任务 04）、`instance.go`、`procgroup_unix.go`、`procgroup_windows.go`（任务 05）、`catalog.go`、`resolve.go`、`detect.go`（任务 06；另有 `catalog/*.yaml`）、`docsync.go`、`diagnostics.go`（任务 07）、`query.go`（任务 09）、`project.go`（任务 15）。

| 包 / 目录 | 新增或改动 | 任务 |
|---|---|---|
| `pkg/config` | `lsp.go`（新）；`config.go`；`load.go` | 01 |
| `pkg/event`、`pkg/tool` | `event/lsp.go`（新）；`tool/search.go`（追加 port） | 02 |
| `pkg/lsp`、`pkg/process`、`pkg/run`、`pkg/tool`、`pkg/architecture` | 包骨架；`open.go`、`runner_pool.go`、`config_reload.go`；`runner.go`、`factory.go`；`registry.go`（`AgentToolRuntime` 字段）；分层表与 `graph.json` | 03 |
| `pkg/lsp` | `jsonrpc.go`、`protocol.go`、`position.go` | 04 |
| `pkg/lsp` | `instance.go`、`procgroup_unix.go`、`procgroup_windows.go` | 05 |
| `pkg/lsp` | `catalog.go`、`catalog/*.yaml`、`resolve.go`、`detect.go` | 06 |
| `pkg/lsp` | `docsync.go`、`diagnostics.go` | 07 |
| `pkg/lsp` | `pool.go`、`manager.go`、`control.go` 的真实实现 | 08 |
| `pkg/lsp` | `query.go` | 09 |
| `pkg/tool`、`pkg/run`、`pkg/safety`、`pkg/tui` | `search.go`（`lsp` 工具）、`file_tools.go`（`authorizeRead`）、`registry.go`、`format.go`；`permissions.go`；`engine.go`、`permission_rules.go`；`render.go` | 10 |
| `pkg/tool`、`pkg/run`、`pkg/tui` | `file_tools.go`、`shell_tool.go`、`state.go`、`format.go`；`config.go`、`runner.go`；`render.go` | 11 |
| `cmd/forebrain`、`pkg/lsp`、`.github/workflows` | `cmd/forebrain/lsp.go`（新）、`root_test.go`；`detect.go`（`Install`、`Doctor`）、`pool.go`（`planLaunch`）、`control.go`（`Manager.Install`）、`manager_test.go`（集成测试）；`lsp-integration.yml`（新） | 12 |
| `pkg/lsp`、`pkg/process`、`pkg/tui`、`pkg/gateway`、`cmd/forebrain`、`frontend` | `control.go`、`manager.go`；`open.go`、`runner_pool.go`；`notify.go`、`run.go`、`chat_session.go`；`api_extra.go`；`lsp.go`（`recommendations reset`）；`useChatStream.ts`、`ChatView.vue`、`lib/lspRecommendation.ts`（新）、`LspRecommendationCard.vue`（新）、`api.ts`、`locales` | 13 |
| `pkg/turn`、`pkg/tui`、`pkg/gateway`、`frontend` | `slash.go`、`executor.go`、`mcp_format.go`、`model.go`；`run.go`、`render.go`、`commands.go`、`chat_slash.go`、`chat_session.go`、`notify.go`；`api_extra.go`、`slash_handlers.go`、`server.go`；`LspView.vue`（新）、路由、导航、`api.ts`、`locales` | 14 |
| `pkg/lsp`、`pkg/process`、`cmd/forebrain`、`pkg/gateway`、`frontend` | `project.go`（新）、`manager.go`、`resolve.go`、`control.go`；`mcp.go`；`mcp_consent.go`、`interactive.go`；`api_extra.go`；`ProjectDetailView.vue`、`api.ts`、`locales` | 15 |
| `pkg/migrate` | `lsp.go`（新）；`claude.go`、`plan.go`、`source.go`、`doc.go` | 16 |
| `pkg/lsp` | `catalog/*.yaml`（新增 27 个，共 38 个）；`catalog.go`、`resolve.go`（`require_root_marker`）；`pkg/migrate/lsp.go` 的映射表 | 17 |
| `README.md`、`FOREBRAIN.md`、`.gitmessage` | 文档；本规范的状态行 | 18 |
