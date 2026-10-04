# LSP 代码智能：任务计划索引

规范：[`docs/plan/LSP_CODE_INTELLIGENCE_PLAN.md`](../LSP_CODE_INTELLIGENCE_PLAN.md)（下称「规范」）。所有任务计划都基于提交 `6305ea9`（2026-09-29）编写。

## 给执行者

1. 一次只执行一个任务。先读完整个任务计划，再读它列出的规范章节。
2. 按依赖顺序执行：依赖的任务必须已合入 `main`。
3. 每一步都有「验证」命令和预期结果；结果不符且一次合理修复后仍不符，按该任务的 STOP 条件停下汇报。
4. 规范与代码冲突时不要改规范，停下汇报。
5. 完成后把下表本任务的状态改为 `DONE`（若派发者说明由其维护索引则跳过）。

## 所有任务通用的命令

| 用途 | 命令 | 成功时 |
|---|---|---|
| 构建 | `CGO_ENABLED=1 go build -tags fts5 ./...` | 退出码 0 |
| 静态检查 | `go vet ./...` | 退出码 0 |
| 格式 | `gofmt -l cmd pkg third_party` | 无输出 |
| 包测试 | `CGO_ENABLED=1 go test -tags fts5 ./pkg/<包>/... -count=1` | `ok` |
| 分层测试 | `CGO_ENABLED=1 go test -tags fts5 ./pkg/architecture -count=1` | `ok` |
| 包图 | `scripts/package-graph.sh && git status --short pkg/architecture/testdata/graph.json` | `graph.json` 记录每个包的 import 关系**和代码行数**，所以任何改动 `pkg/` 下 Go 生产代码的任务都会出现 ` M`，必须一并提交（CI 会 `git diff --exit-code` 这个文件；脚本依赖 `jq`） |
| 全量测试 | `CGO_ENABLED=1 go test -tags fts5 ./... -count=1 -timeout 20m` | 全部 `ok` |
| 前端（仅改前端的任务） | `cd frontend && pnpm install --frozen-lockfile && pnpm test && pnpm build` | 退出码 0；**之后执行 `git checkout -- pkg/gateway/dist`** |
| 提交标题自检 | `scripts/check-commit-message.sh <消息文件>` | 退出码 0 |

## 通用 Git 流程

- 分支：从最新 `main` 切 `feat/lsp-<编号>-<slug>`。
- 提交标题：`<type>(<scope>): <小写英文祈使句，无句号，≤100 字符>`，例如 `feat(lsp): add the json-rpc transport and position mapping`。scope 取主要改动的包名（新包用 `lsp`）。
- 正文按 `.gitmessage`：`Why:` / `What:` / `Verification:`（只写实际运行过的命令）；结尾带 `Refs: docs/plan/lsp/<任务文件名>`。
- 签名：`git commit -s -F <消息文件>`。
- **不得提交 `pkg/gateway/dist`**。
- 未经操作者要求，不要 push、不要开 PR。

## 执行顺序与状态

| # | 任务 | 优先级 | 工作量 | 依赖 | 状态 |
|---|---|---|---|---|---|
| 01 | [`lsp` 配置段与 `features.lsp`](01-config.md) | P0 | S | — | DONE（基于 1a6d708 复核锚点后执行；未提交） |
| 02 | [port 接口与 DTO](02-ports-and-dtos.md) | P0 | S | — | DONE（未提交） |
| 03 | [`pkg/lsp` 骨架与组合根接线](03-skeleton-wiring.md) | P0 | M | 01、02 | DONE（未提交） |
| 04 | [协议层：JSON-RPC、协议类型、位置换算、URI](04-protocol.md) | P0 | M | 03 | DONE（未提交） |
| 05 | [服务器实例：进程、初始化、服务器请求、就绪、重启、进程树](05-instance.md) | P0 | L | 04 | DONE（未提交） |
| 06 | [目录、配置合并、根解析、二进制探测](06-catalog-resolve-detect.md) | P0 | M | 03 | DONE（未提交） |
| 07 | [文档同步与诊断存储](07-docsync-diagnostics.md) | P0 | M | 05 | DONE（未提交；NewProblems 按规范 §8.3.4「取靠后条目」，计划算法描述与规范矛盾，以规范为准） |
| 08 | [实例池与 Manager 的诊断侧和控制面](08-pool-manager.md) | P0 | L | 06、07 | DONE（未提交） |
| 09 | [查询操作与结果格式化](09-query.md) | P0 | L | 08 | DONE（未提交） |
| 10 | [`lsp` 工具与权限](10-lsp-tool.md) | P1 | L | 09 | DONE（未提交） |
| 11 | [编辑后诊断与迟到提醒](11-edit-diagnostics.md) | P1 | L | 08 | DONE（未提交） |
| 12 | [`forebrain lsp` 命令、安装、集成测试](12-cli-install-integration.md) | P2 | M | 09 | DONE（未提交；真机实测 gopls+clangd×2+jdtls 全 PASS） |
| 13 | [推荐](13-recommendation.md) | P2 | L | 11、12、14 | DONE（未提交；真机验证：真实 gopls 探测→推荐→enable→预启动全通） |
| 14 | [`/lsp` 面板、Web 页面、`/status`](14-lsp-panel.md) | P2 | L | 12 | DONE（未提交；Web 侧按现行架构落在 Settings 的 LspTab，非独立路由页） |
| 15 | [项目级配置与同意](15-project-config.md) | P2 | M | 08 | DONE（未提交；Web 侧按现行架构落在 components/project/ProjectLsp.vue） |
| 16 | [`/migrate` 导入](16-migrate.md) | P2 | M | 01 | DONE（未提交；官方 marketplace.json + plugins-reference 核对通过） |
| 17 | [扩展语言与诊断型服务器](17-extended-catalog.md) | P2 | M | 06 | DONE（未提交；27 个条目全部对照官方文档核实；顺带根因修复 control.go 既有数据竞争） |
| 18 | [文档](18-docs.md) | P2 | S | 全部 | DONE（未提交） |

状态取值：`TODO` | `IN PROGRESS` | `DONE` | `BLOCKED（一句原因）`

## Reconcile 记录

- **2026-10-04（严格复审）**：按 owner 指令对全部 18 项计划 × 全部未提交改动做逐字严格审查（10 路并行审查：每任务对照计划+规范+纯代码审查）。判定 18/18 忠实实现、无计划要求缺失；发现并已根因修复 1×P1 + 10×P2 + 1×P3，全部在 `pkg/lsp` 内：instance.go 的 Shutdown/Restart/crash-restart 并发簇（P1 死锁：Restart 进行中 Shutdown 永久阻塞；done 关闭非终态忙转；Shutdown 后 gen 复活泄漏；终态被 setState 覆写——统一为 doneClosed 终态单一权威）；resolve.go `hasRootMarker`/`markersExistBetween` 补 EvalSymlinks 归一 + pathWithin 越界守卫（§7.4 同规则）；pool.go MaxServers 改按不同实例数计（§7.5 语义）；manager.go git sweep 对「离开 git status 的路径」先 `os.Stat`——仍存在记 changed(2)、真消失才 deleted(3)（规范无此定义，git add/commit 场景旧实现会把存活文件报 deleted 致诊断被丢）；`instanceForServer` 优先选 `docs.IsOpen(path)` 的实例（产生诊断的那份内容/编码）；diagnostics.go 排序前预计算行列（原 O(n log n) 次整文件读盘）；query.go 位置列表改按文件稳定分组（组序=首次出现，计划 09 步骤 4 语义，交错返回时旧实现重复文件头/文件数错）；detect.go csharp-ls 恢复提示 glob 由 `root/obj/*/project.assets.json` 改为 `root/*/obj/project.assets.json`；control.go `Install` 起点改立即通知 `notifyNow`（原 100ms 去抖吞掉 running=true 边沿，快速失败时终端无结局+订阅泄漏）；manager.go `expandBraces` 按配对花括号递归切分（并列组 `x{1,2}/y{a,b}` 原切错）。每项均带回归测试；验证：`go build`/`go vet`/`gofmt` 全绿，`pkg/lsp -race` 生命周期子集通过，全量 `CGO_ENABLED=1 go test -tags fts5 ./... -count=1` 全部 `ok`，`package-graph.sh` 已重生成。保留未修（P3，计划内行为/规范自定/无触发点，逐项已记录）：server id 折叠小写在 validate 之前（大写 id 静默归一为计划设计，仅 pre-fold 碰撞角落未定义）、jsonrpc `omitempty` 缝隙（19 个调用点均不触发）、consent 存储不可读时从空重写（沿用 MCP 孪生模式，单独改 LSP 会双标）、apply_patch 卡片诊断双显与 render.go pending 行误探（计划文本固有）、其余见本轮审查记录。
- **2026-10-04**：18 项 DONE 全部抽检通过——结构判据（pkg/lsp 17 生产文件、38 目录 YAML、7 个满额包各 20 文件、全部界面/CLI/工作流/前端文件在位）、判据 grep（`errNotAvailable` 0 处、`wrapLSPDiagnosticsReminderLLM` 1 处、`pkg/lsp` 导入恰 4 包、migrate `"lsp"` 恰 2 处、README/FOREBRAIN/规范状态行）、测试子集（config/event/architecture/tool/migrate/lsp 六包 `ok`）。工作区自执行完成以来无漂移（HEAD 仍为 `1a6d708`，改动未提交，待 owner 审阅）。无 BLOCKED / 遗留 TODO；下一批次为 P3（见文末），需要时另写计划。

## 依赖说明

- **为什么骨架先行（03）**：`TestEveryPackageHasAnImporter` 要求每个包从出现起就有导入方，所以 `pkg/lsp` 必须和它在 `pkg/process` 里的接线同一个 PR 出现。骨架的 Manager 什么也不启动，用户行为不变；之后的任务在已接好的接口后面逐步填实现。
- 04 → 05 → 07 在 `pkg/lsp` 内逐层叠加（传输 → 实例 → 文档与诊断）；06 与它们并行（目录与配置合并）；08 把两条线合起来。
- 10 与 11 是模型可见的变化：10 在有已启用服务器时新增 `lsp` 工具；11 让编辑工具的结果可能带诊断。
- 13 依赖 11（触发点在 `DidWrite` 路径上）、12（安装实现）与 14（Web 卡片轮询安装进度用的 `GET /api/v1/lsp` 与 API 客户端）。所以实际执行顺序是 12 → 14 → 13。
- 16 只依赖 01：`pkg/migrate` 不能导入 `pkg/lsp`，只写 `config` 结构。

## 结构约束速查（违反会让 `pkg/architecture` 测试失败）

- 每包最多 20 个生产文件。**`tool`、`run`、`process`、`turn`、`tui`、`gateway`、`safety` 已满，只能改已有文件**；`config` 在任务 01 后也满。
- `pkg/lsp` 不能有子包；`testdata` 下不能放 `.go` 文件。
- `X_test.go` 必须对应 `X.go`。
- 不给 `run.Runner` 加方法或字段；需要的状态放 `run.Deps`。
- fan-out 已到上限、不能新增导入的包：`tool`、`run`、`tui`、`gateway`、`migrate`；`process` 由任务 03 从 18 调到 19 后同样不能再加。界面与工具只经 `pkg/tool` 的 port 与 `pkg/event` 的 DTO 访问 LSP。
- `pkg/lsp` 只能导入 `config`、`home`、`event`、`tool`。

## 不在本批次（P3，开始前另写计划）

长驻进程沙箱、`rename_preview` / `code_actions` 预览、`fsnotify` 清单监听与全项目诊断、C# Roslyn LS、Swift Xcode 工程引导。见规范 §12.2。
