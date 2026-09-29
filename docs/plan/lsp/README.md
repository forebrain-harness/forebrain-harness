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
| 01 | [`lsp` 配置段与 `features.lsp`](01-config.md) | P0 | S | — | TODO |
| 02 | [port 接口与 DTO](02-ports-and-dtos.md) | P0 | S | — | TODO |
| 03 | [`pkg/lsp` 骨架与组合根接线](03-skeleton-wiring.md) | P0 | M | 01、02 | TODO |
| 04 | [协议层：JSON-RPC、协议类型、位置换算、URI](04-protocol.md) | P0 | M | 03 | TODO |
| 05 | [服务器实例：进程、初始化、服务器请求、就绪、重启、进程树](05-instance.md) | P0 | L | 04 | TODO |
| 06 | [目录、配置合并、根解析、二进制探测](06-catalog-resolve-detect.md) | P0 | M | 03 | TODO |
| 07 | [文档同步与诊断存储](07-docsync-diagnostics.md) | P0 | M | 05 | TODO |
| 08 | [实例池与 Manager 的诊断侧和控制面](08-pool-manager.md) | P0 | L | 06、07 | TODO |
| 09 | [查询操作与结果格式化](09-query.md) | P0 | L | 08 | TODO |
| 10 | [`lsp` 工具与权限](10-lsp-tool.md) | P1 | L | 09 | TODO |
| 11 | [编辑后诊断与迟到提醒](11-edit-diagnostics.md) | P1 | L | 08 | TODO |
| 12 | [`forebrain lsp` 命令、安装、集成测试](12-cli-install-integration.md) | P2 | M | 09 | TODO |
| 13 | [推荐](13-recommendation.md) | P2 | L | 11、12、14 | TODO |
| 14 | [`/lsp` 面板、Web 页面、`/status`](14-lsp-panel.md) | P2 | L | 12 | TODO |
| 15 | [项目级配置与同意](15-project-config.md) | P2 | M | 08 | TODO |
| 16 | [`/migrate` 导入](16-migrate.md) | P2 | M | 01 | TODO |
| 17 | [扩展语言与诊断型服务器](17-extended-catalog.md) | P2 | M | 06 | TODO |
| 18 | [文档](18-docs.md) | P2 | S | 全部 | TODO |

状态取值：`TODO` | `IN PROGRESS` | `DONE` | `BLOCKED（一句原因）`

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
