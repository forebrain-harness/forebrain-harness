# 任务 09：`Query`——导航查询与结果格式化

> **执行者须知**：逐步执行；每一步运行「验证」命令并确认结果后再进入下一步。出现「STOP 条件」中的任何情况立即停止并汇报。完成后把 `docs/plan/lsp/README.md` 中本任务状态改为 `DONE`。
>
> **规范**：`docs/plan/LSP_CODE_INTELLIGENCE_PLAN.md`。开始前读规范 §7.7（索引中的请求）、§8.1、§8.2、附录 B.5、B.6、附录 C（全部）。
>
> **前置**：任务 08 已合入。先确认 `grep -n "func (p \*Pool) acquire" pkg/lsp/pool.go` 有输出。
>
> **漂移检查（先运行）**：`git diff --stat 6305ea9..HEAD -- pkg/lsp/manager.go pkg/lsp/protocol.go pkg/lsp/position.go`，确认任务 04/08 定义的函数名与本计划引用的一致。

## 状态

- 优先级：P0 · 工作量：L · 风险：LOW（无外部调用方，任务 10 才接到工具上）
- 依赖：08
- 类别：direction
- 计划基于：`6305ea9`，2026-09-29

## 为什么

`lsp` 工具的每一个操作最终都是 `Manager.Query`：选服务器、确保实例就绪、同步文档、把 1-based 行列（或符号）换成 LSP 位置、按操作发请求、把结果换成模型可读的文本（附录 B.5）并给出界面字段。所有错误文本逐字取自附录 C。

## 现状

- `pkg/lsp/manager.go` 的 `Query` 仍是任务 03 的骨架（返回 `untrusted` 或 `no-server` 错误）。
- 可用的零件：`ServersForFile`、`ResolveRoot`（任务 06）；`pool.acquire`、`begin/end`、`EffectiveLSP().RequestTimeout`（任务 08）；`DocSync.EnsureSynced`、`Content`、`PullDiagnostics`、`DiagStore.Get`、`FormatEditDiagnostics` 的行格式（任务 07，可抽出一个行格式化函数复用）；`ToLSPPosition`、`FromLSPPosition`、`AnchorSymbol`、`LineText`、`LineCount`、`PathToURI`、`URIToPath`、`*LineOutOfRangeError`、`*SymbolNotFoundError`（任务 04）；`DecodeLocations`、`DecodeHover`、`DecodeDocumentSymbols`、`SymbolKindName`、`CallHierarchyItem` 等（任务 04）；`Instance.Capabilities().Supports`、`WaitReady`、`State`（任务 05）。
- `tool.CodeIntelQuery` / `tool.CodeIntelResult` / `tool.LSPOp*` 常量（任务 02）。
- 结构约束：`pkg/lsp` 目前 15 个生产文件，本任务新建 `query.go` 后 16 个。

## 范围

**新建：** `pkg/lsp/query.go`、`pkg/lsp/query_test.go`。**修改：** `pkg/lsp/manager.go`（`Query` 改为调用 `query.go`）、`pkg/lsp/manager_test.go`（删除已不成立的骨架 `Query` 断言）、`pkg/lsp/diagnostics.go`（只允许把单行格式化抽成未导出函数供复用，不改输出）、`pkg/lsp/doc.go`、`graph.json`。

**不要碰：** `pkg/tool`（任务 10）、其他 `pkg/lsp` 文件的行为。

## 步骤

### 步骤 1：参数校验与服务器选择

`func (m *Manager) Query(ctx context.Context, q tool.CodeIntelQuery) (tool.CodeIntelResult, error)`：

1. `!m.opts.Trusted` → 附录 C `untrusted`。`q.Operation` 不在 `tool.LSPOperations` → 附录 C `unknown-operation`。
2. 需要位置的操作（definition、declaration、type_definition、implementation、references、hover、incoming_calls、outgoing_calls、supertypes、subtypes）：缺 `AbsPath`、`Line <= 0`、或 `Column <= 0 && Symbol == ""` → 附录 C `missing-args`。`document_symbols` 缺 `AbsPath` → 同样文本。`workspace_symbols` 缺 `Query` → `missing-query`。
3. 有 `AbsPath` 时：不在项目根内 → `outside-project`（`{path}` 用 `q.DisplayPath`，为空用 `AbsPath`）；`ServersForFile` 没有 primary → `no-server`（`{ext}` 为小写扩展名，没有扩展名时用文件名）。
4. `workspace_symbols` 无 `AbsPath`：在本 manager 引用的**已就绪** primary 实例中按服务器 ID 字典序取第一个；没有 → `no-running-server`。
5. `ctx, cancel := context.WithTimeout(ctx, EffectiveLSP().RequestTimeout)`。`entry, err := pool.acquire(ctx, m, srv, root)`；错误原样返回（已是附录 C 文本：`not-installed`、`start-failed`、`too-many-servers`）；ctx 超时 → `request-timeout`。
6. `begin(entry)`/`defer end(entry)`。等就绪：`inst.WaitReady(ctx)` 在剩余时间的一半内没有就绪 → 继续执行，但在结果首行加附录 B.6 的索引提示。

### 步骤 2：位置

1. `_, content, err := docs.EnsureSynced(ctx, abs)`：`ErrFileTooLarge` → 附录 C `file-too-large`；其他错误原样。
2. `Symbol != ""`：`AnchorSymbol(content, Line, Symbol)`；`*SymbolNotFoundError` → 附录 C `symbol-not-found`（字段取自错误）；找到的行与原行不同 → 提示行 `(symbol found on line X)`。用找到的行与列。
3. `ToLSPPosition(content, line, column, inst.Encoding())`：`*LineOutOfRangeError` → `line-out-of-range`；`clamped` → 提示行 `(column clamped to end of line)`。
4. 提示行顺序固定：索引 → 列 → 符号（附录 B.6）。

### 步骤 3：按操作发请求

能力检查（缺能力 → 附录 C `unsupported`，`{operation}` 用 `q.Operation`）：

| operation | 能力键 | 方法与参数 |
|---|---|---|
| definition | `definitionProvider` | `textDocument/definition`，`TextDocumentPositionParams` |
| declaration | `declarationProvider` | `textDocument/declaration` |
| type_definition | `typeDefinitionProvider` | `textDocument/typeDefinition` |
| implementation | `implementationProvider` | `textDocument/implementation` |
| references | `referencesProvider` | `textDocument/references`，`ReferenceParams`（`IncludeDeclaration`） |
| hover | `hoverProvider` | `textDocument/hover` |
| document_symbols | `documentSymbolProvider` | `textDocument/documentSymbol {"textDocument"}` |
| workspace_symbols | `workspaceSymbolProvider` | `workspace/symbol {"query"}`；返回项的 `location` 缺 `range` 且服务器支持 `workspaceSymbolProvider.resolveProvider` 时对前 `MaxResults` 项逐个 `workspaceSymbol/resolve` |
| incoming_calls / outgoing_calls | `callHierarchyProvider` | `textDocument/prepareCallHierarchy`，取第一项，再 `callHierarchy/incomingCalls` / `outgoingCalls {"item"}` |
| supertypes / subtypes | `typeHierarchyProvider` | `textDocument/prepareTypeHierarchy`，取第一项，再 `typeHierarchy/supertypes` / `subtypes {"item"}` |
| diagnostics | —（总是可用） | 有 `AbsPath`：`PullDiagnostics` 返回 true 用其结果，否则读存储；存储没有本版本的结果时 `WaitFor` 最多 `min(剩余时间, 2s)`。无 `AbsPath`：汇总本 manager 所有实例已打开文档在存储里的问题 |

请求被 ctx 超时打断 → 附录 C `request-timeout`（`{n}` 为 `RequestTimeout` 秒数）；服务器返回 JSON-RPC 错误（`*RPCError`）→ 附录 C `server-error`。

### 步骤 4：结果格式（逐字照附录 B.5）

实现未导出的格式化函数，每个返回 `(text string, count int, files int)`：

- **位置列表**（前五种操作）：`DecodeLocations` → 每个位置：`path := URIToPath(uri)`；项目根内显示相对路径（`/` 分隔），项目外显示绝对路径并在行末加 `  [outside project]`；行列用 `FromLSPPosition`（内容来自 `docs.Content`，否则在 `q.PreviewAllowed(path)` 为真且文件 ≤ 4 MiB 时读盘，否则直接 `+1`）；预览行只在 `PreviewAllowed(path)` 为真时给出，取 `LineText` 去首尾空白、截到 160 个字符（按码点）。去重（同路径同行列）；`references` 按路径、行、列排序，其余保持服务器顺序；按文件分组（组的顺序即第一次出现的顺序）；超过 `MaxResults` 截断并加截断行。首行 `{label} \`{target}\`: {N} result{s} in {M} file{s}`，`{target}` 为 `Symbol`（给了时）否则 `{line}:{column}`；`{s}` 在数量为 1 时为空。空结果：`no results for {operation} at {relative path}:{line}:{column}`。
- **hover**：`DecodeHover` 为空 → `no hover information at …`；否则首行 `hover at {relative path}:{line}:{column}` + 换行 + 内容，内容超过 2000 个字符截断并加 `…`。
- **document_symbols**：层级型递归输出，缩进每层两个空格；扁平型（`SymbolInformation`）按 `ContainerName` 不缩进；行范围用 `range.start.line+1` 与 `range.end.line+1`；超过 `MaxResults` 截断。
- **workspace_symbols**、**calls**、**hierarchy**：按附录 B.5 的首行与行格式；位置规则同上（不给预览行）。
- **diagnostics**：首行按附录 B.5；问题行复用任务 07 的单行格式化（severity、行列、message、`[source code]`）；按 `min_severity` 过滤。
- 提示行（若有）放在首行之前，每行一条。

### 步骤 5：界面字段

`CodeIntelResult.Display` 填：`"operation"`、`"server"`（服务器 ID）、`"result_count"`、`"files"`、`"elapsed_ms"`（从进入 `Query` 起算）、`"indexing"`（bool）、`"target"`（`Symbol` 或 `line:column`）、`"display_path"`。

### 步骤 6：更新 `doc.go`，写测试

`doc.go` 文件清单加：`query.go (the lsp tool's lookups and their model-facing text)`。

**验证**：`CGO_ENABLED=1 go test -tags fts5 ./pkg/lsp -count=1 -race -timeout 8m` → `ok`；`gofmt`、`go vet` 干净；`graph.json` 重新生成并提交；`CGO_ENABLED=1 go test -tags fts5 ./... -count=1 -timeout 20m` → 全部 `ok`。

## 测试计划

`query_test.go`，用假服务器（`responses` 返回固定结果）与自定义服务器配置（同任务 08 的测试写法）：

1. `TestQueryArgumentErrors`：每个附录 C 参数类错误（`missing-args`、`missing-query`、`outside-project`、`no-server`、`untrusted`、`unknown-operation`）逐字比对；`responses` 让服务器回 JSON-RPC 错误时为 `server-error` 文本。
2. `TestQueryDefinitionGolden`：`responses["textDocument/definition"]` 为一个 Location → 输出与附录 B.5 格式逐字一致（含预览行）；record 中请求的 `position` 正确（用含中文的行验证 UTF-16 换算）。
3. `TestQuerySymbolAnchor`：`Symbol` 在下一行 → 首行前有 `(symbol found on line X)`；找不到 → `symbol-not-found`。
4. `TestQueryReferencesSortedGroupedTruncated`：三个文件五个位置、`MaxResults: 3` → 分组、排序、截断行正确；项目外位置带 `[outside project]` 且无预览；`PreviewAllowed` 返回 false 的项目内文件也无预览。
5. `TestQueryLocationLink`：`LocationLink` 数组被正确解码。
6. `TestQueryHover`：MarkupContent、超过 2000 字符截断、空结果文本。
7. `TestQueryDocumentSymbolsHierarchical`、`TestQueryDocumentSymbolsFlat`。
8. `TestQueryWorkspaceSymbolsNeedsRunningServer`：没有运行中实例 → `no-running-server`；有则返回结果。
9. `TestQueryCallHierarchy`：prepare → incoming 两步都在 record 中，输出首行 ``callers of `name`: N``。
10. `TestQueryUnsupported`：capabilities 无 `typeHierarchyProvider` → `unsupported` 文本。
11. `TestQueryTimeout`：`hang: ["textDocument/hover"]`、`request_timeout: 1` → 约 1 秒后返回 `request-timeout` 文本。
12. `TestQueryIndexingNote`：`Readiness: progress` 且进度一直不结束 → 结果首行为索引提示。
13. `TestQueryDiagnostics`：有文件时 pull 与 push 两种模式；无文件时汇总。
14. `TestQueryNotInstalled`：自定义服务器命令不存在 → `not-installed` 文本。

## 完成判据

- [ ] `grep -n "no language server handles" pkg/lsp/manager.go` 无输出（骨架错误已移到 `query.go` 的正式实现里）
- [ ] 上述 14 个测试通过；`-race` 通过
- [ ] `ls pkg/lsp/*.go | grep -v _test.go | wc -l` 为 16
- [ ] `graph.json` 已提交；全量测试通过
- [ ] README 中任务 09 状态为 `DONE`

## STOP 条件

- 附录 B.5 的某种形态无法实现（例如服务器返回的数据不足以产生规定的行），需要改规范。
- 需要改动 `pkg/tool`。

## 维护说明

- 输出文本进入模型上下文，改格式必须同步规范附录 B.5 与 golden 测试。
- 新增操作时要同时改：`tool.LSPOperations`（任务 02 的常量）、工具 schema 的枚举（任务 10）、本文件的分派表、规范 §8.1。
