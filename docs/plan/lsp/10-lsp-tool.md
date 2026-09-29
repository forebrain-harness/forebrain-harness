# 任务 10：`lsp` 工具、权限映射与工具卡片

> **执行者须知**：逐步执行；每一步运行「验证」命令并确认结果后再进入下一步。出现「STOP 条件」中的任何情况立即停止并汇报。完成后把 `docs/plan/lsp/README.md` 中本任务状态改为 `DONE`。
>
> **规范**：`docs/plan/LSP_CODE_INTELLIGENCE_PLAN.md`。开始前读规范 §3.2、§3.11、§8.1、§8.5、§8.6、§9.4、§11.2、附录 B.1、B.2。
>
> **前置**：任务 09 已合入（`Manager.Query` 是真实实现）。
>
> **漂移检查（先运行）**：`git diff --stat 6305ea9..HEAD -- pkg/tool/registry.go pkg/tool/file_tools.go pkg/tool/format.go pkg/run/permissions.go pkg/safety/engine.go pkg/safety/permission_rules.go pkg/tui/render.go`。任务 02、03 会改 `search.go`、`registry.go`；其余文件若有变化，对比「现状」摘录。

## 状态

- 优先级：P1 · 工作量：L · 风险：MED（**模型可见变化**：有已启用服务器的会话多一个工具）
- 依赖：09
- 类别：direction
- 计划基于：`6305ea9`，2026-09-29

## 为什么

这是模型第一次能用到 LSP：一个只读的 `lsp` 工具。它必须 ① 只在冻结决定为真时注册，且描述与 schema 字节稳定；② 对 `file_path` 做与 `read_file` 完全相同的授权；③ 在权限层按 `Read` 规则评估；④ 在两个界面都有可读的卡片。

## 现状

- `pkg/tool/registry.go:85-165 RegisterDefaultTools`：先对每个工具 `registerMeta(tool, desc, category, event.ToolMeta{…})`，最后按固定顺序 `st.Register`：`t1, t2, t3`（read/write/edit）→ `t7`（shell）→ `tRetrieve` → `t16`（web_fetch）→ `t17`（web_search）→ `t21`（request_permissions，可选）→ `registerSessionAndMemoryTools`。
- `pkg/tool/registry.go:264` `AgentToolRuntime` 已有 `CodeIntel CodeIntelligence`、`CodeIntelTool bool`（任务 03）。
- `pkg/tool/file_tools.go:49-178 NewFileReadTool`，其中 `:60-105` 是读取授权：

  ```go
  resolution, err := resolveReadFilePath(ctx, st, in.FilePath)
  if err != nil {
  	if errors.Is(err, ErrPathNotAllowed) {
  		CaptureToolError(ctx, err)
  	}
  	return "", err
  }
  abs := resolution.Abs
  ... // skill 标识（read_file 专属，lsp 不需要）
  if isBlockedReadDevicePath(abs) {
  	return "", fmt.Errorf("refusing to read blocking or infinite device path")
  }
  if reason := st.ProtectedReadReason(abs); reason != "" && ApprovedActionIDFromContext(ctx) == "" {
  	hook := st.ActionHook()
  	if hook == nil { ... ErrPathNotAllowed ... }
  	payload := map[string]any{"file_path": in.FilePath, "resolved_file_path": abs}
  	markProtectedApproval(payload, reason)
  	id, pending, err := hook(ctx, "read_file", payload)
  	...
  	if pending && id != "" {
  		CaptureToolRequiresAction(ctx, id, "read_file", map[string]any{"requires_action": true})
  		return "", &RequiresActionError{ActionID: id, ActionKind: "read_file", ToolName: "read_file", ToolInput: payload}
  	}
  }
  if st.ReadPathDenied(abs) && !st.PathUnderLoadedSkillRoot(abs) {
  	CaptureToolError(ctx, ErrPathReadDenied)
  	return "", fmt.Errorf("%w: %s", ErrPathReadDenied, abs)
  }
  ```

- `pkg/tool/permissions.go:202 resolveReadFilePath`、`:218 resolveFileToolPath`（用 `MergeAllowedRootPaths(st.AllowedRoots(), st.PermissionRoots(access))` 与 `ResolveWithinRoots` 判断路径是否在可读根内）。
- `pkg/tool/format.go:27-111 FormatToolStepResult`（按工具名分派 `format*Step`）；`:213 SummarizeToolStep`。
- `pkg/run/permissions.go:655-693`：

  ```go
  func permissionToolName(kind string) string {
  	k := strings.TrimSpace(kind)
  	if strings.EqualFold(k, "shell") {
  		return "Bash"
  	}
  	return k
  }

  func permissionInput(kind string, payload map[string]any) string {
  	...
  	case strings.EqualFold(k, "read_file"),
  		strings.EqualFold(k, "write_file"),
  		strings.EqualFold(k, "edit_file"),
  		strings.EqualFold(k, "multi_edit"):
  		if p, ok := payload["file_path"].(string); ok {
  			return strings.TrimSpace(p)
  		}
  	...
  }
  ```

  `evaluateToolPermission`（:714）用 `permissionToolName(kind)` 与 `permissionInput(kind, payload)` 调 `EvaluatePermissionForSession`。
- `pkg/safety/permission_rules.go:338 canonicalToolAliases`（`"read_file": "Read"` 等）；`pkg/safety/engine.go:406 safeReadOnlyTools`（`"Read": {}` 等，带注释说明为何安全）。
- `pkg/tui/render.go:2253 failedActionPhrase`、`:2507 toolDisplayParts`（按工具名给出 `action` 与 `target`，例如 `web_fetch` 分支用 `inputString(meta, "url")`）；`:3091 inputString`、`:3124 inputInt`。
- 测试范例：`pkg/tool/request_permissions_test.go:640 TestRegisterDefaultToolsHasCorrectCategories`（`NewState` + `agent.New(noopLLM{}, …)` + `RegisterDefaultTools` + `st.ToolMetaByName`）。
- 结构约束：`tool`、`run`、`safety`、`tui` 都已满 20 个生产文件，**只能改已有文件**；`lsp` 工具写在 `pkg/tool/search.go`，测试写在 `pkg/tool/search_test.go`。

## 范围

**修改：** `pkg/tool/search.go`、`search_test.go`、`registry.go`、`registry_test.go`、`file_tools.go`、`file_tools_test.go`、`format.go`、`format_test.go`；`pkg/run/permissions.go`、`permissions_test.go`、`runner_test.go`；`pkg/safety/permission_rules.go`、`permission_rules_test.go`、`engine.go`、`engine_test.go`；`pkg/tui/render.go`、`render_test.go`；`graph.json`。

**不要碰：** `pkg/lsp`、写工具的诊断（任务 11）、系统提示与任何 LLM wrapper（本任务**不得**修改系统提示）。

## 步骤

### 步骤 1：抽出 `authorizeRead`（`file_tools.go`）

在 `file_tools.go` 新增：

```go
// authorizeRead settles whether toolName may read filePath, the same way for
// every reading tool: path resolution against the readable roots, blocking
// device paths, the FOREBRAIN_HOME approval, and deny rules.
func authorizeRead(ctx context.Context, st *State, toolName, filePath string) (readPathResolution, error)
```

把 `NewFileReadTool` 中 `:60-105` 的逻辑搬进去（`read_file` 专属的 skill 标识那几行留在 `read_file` 里，放在调用 `authorizeRead` 之后、原位置不变），把硬编码的 `"read_file"`（hook 的 kind、`CaptureToolRequiresAction` 的 kind、`RequiresActionError` 的 `ActionKind`/`ToolName`）换成 `toolName`。`NewFileReadTool` 改为调用 `authorizeRead(ctx, st, "read_file", in.FilePath)`。**行为必须完全不变**：先运行一遍 `read_file` 相关测试确认。

**验证**：`CGO_ENABLED=1 go test -tags fts5 ./pkg/tool -count=1` → `ok`（改动前后都跑一遍，结果相同）。

### 步骤 2：`search.go` —— 工具实现

```go
// lspToolDescription is appendix B.1 verbatim. It is part of the prompt
// prefix: never interpolate anything into it.
const lspToolDescription = "Look up code through the project's language servers: definitions, declarations, type definitions, implementations, references, hover type information, document and workspace symbols, call and type hierarchies, and current diagnostics. Read-only. Lines are 1-based and numbered the way file reads show them. Pass symbol (a name on that line) instead of column when you are not sure of the exact column. Prefer this over text search when you need where a symbol is defined or used."

type LSPInput struct {
	Operation          string `json:"operation" jsonschema:"enum=definition,enum=declaration,enum=type_definition,enum=implementation,enum=references,enum=hover,enum=document_symbols,enum=workspace_symbols,enum=incoming_calls,enum=outgoing_calls,enum=supertypes,enum=subtypes,enum=diagnostics" jsonschema_description:"What to look up."`
	FilePath           string `json:"file_path,omitempty" jsonschema_description:"Absolute or workspace-relative file path. Required for every operation except workspace_symbols."`
	Line               int    `json:"line,omitempty" jsonschema:"minimum=1" jsonschema_description:"1-based line number, as shown by file reads."`
	Column             int    `json:"column,omitempty" jsonschema:"minimum=1" jsonschema_description:"1-based column in characters. Omit when symbol is given."`
	Symbol             string `json:"symbol,omitempty" jsonschema_description:"A name on the given line to position on, used instead of column."`
	Query              string `json:"query,omitempty" jsonschema_description:"Symbol name or prefix to search for. Required for workspace_symbols."`
	IncludeDeclaration bool   `json:"include_declaration,omitempty" jsonschema_description:"For references: also return the declaration itself."`
	MaxResults         int    `json:"max_results,omitempty" jsonschema:"minimum=1,maximum=200" jsonschema_description:"Maximum locations to return (default 50, at most 200)."`
}

// NewLSPTool builds the lsp tool over rt.CodeIntel.
func NewLSPTool(st *State, rt *AgentToolRuntime) (*llm.Tool, error)
```

描述字符串与各字段描述**逐字**等于规范附录 B.1、B.2。处理函数：

1. `rt == nil || rt.CodeIntel == nil` → 返回错误 `language servers are not available in this session`（理论上不会发生，因为不注册）。
2. `in.Operation` 去空白小写。`FilePath` 非空时 `res, err := authorizeRead(ctx, st, "lsp", in.FilePath)`，错误原样返回；`abs = res.Abs`。
3. `MaxResults`：0 → 50；> 200 → 200。
4. `PreviewAllowed`：`func(p string) bool`——`ResolveWithinRoots(p, MergeAllowedRootPaths(st.AllowedRoots(), st.PermissionRoots(safety.FileSystemAccessRead)))` 成功、`!st.ReadPathDenied(p)`、`st.ProtectedReadReason(p) == ""` 三者都满足才为 true。
5. `res, err := rt.CodeIntel.Query(ctx, CodeIntelQuery{…})`；错误：`CaptureToolError(ctx, err)` 后原样返回。
6. `CaptureToolOutput(ctx, res.Display)` 之前往 `Display` 补 `"operation"`、`"file_path": in.FilePath`、`"line"`、`"symbol"`（非空时），然后返回 `res.Text`。

### 步骤 3：注册（`registry.go`）

在 `RegisterDefaultTools` 中，`t21` 的 meta 注册之后加：

```go
	var tLSP *llm.Tool
	if rt != nil && rt.CodeIntel != nil && rt.CodeIntelTool {
		tLSP, err = NewLSPTool(st, rt)
		if err != nil {
			return err
		}
		registerMeta(tLSP, lspToolDescription, "code", event.ToolMeta{ReadOnly: true, ConcurrencySafe: true})
	}
```

并在 `if t21 != nil { _ = st.Register(a, t21) }` 之后、`return registerSessionAndMemoryTools(...)` 之前加 `if tLSP != nil { _ = st.Register(a, tLSP) }`。

**验证**：`CGO_ENABLED=1 go build -tags fts5 ./...` → 退出码 0。

### 步骤 4：权限映射

- `pkg/run/permissions.go`：
  - `permissionToolName(kind)` 改为接收 payload：新增 `permissionToolNameFor(kind string, payload map[string]any) string`：`lsp` 且 `strings.TrimSpace(payload["file_path"])` 非空 → `"Read"`；`lsp` 其余情况 → `"LSP"`；其他沿用 `permissionToolName`。`evaluateToolPermission`（:714）改用它。其余调用 `permissionToolName` 的地方不变。
  - `permissionInput`：`case` 列表加 `strings.EqualFold(k, "lsp")`，与 `read_file` 同一分支取 `file_path`。
- `pkg/safety/permission_rules.go`：`canonicalToolAliases` 在「Discovery」组后加 `"lsp": "LSP",`（注释：`// Code intelligence`）。
- `pkg/safety/engine.go`：`safeReadOnlyTools` 加：

  ```go
  	// LSP asks the project's language servers about code the session can
  	// already read. A call that names a file is evaluated as Read (the
  	// permission layer maps it), so Read rules govern it; what reaches this
  	// entry is only the file-less workspace symbol search.
  	"LSP": {},
  ```

**验证**：`CGO_ENABLED=1 go test -tags fts5 ./pkg/run ./pkg/safety -count=1` → `ok`。

### 步骤 5：卡片正文与标题

- `pkg/tool/format.go`：`FormatToolStepResult` 的 `switch toolName` 加 `case "lsp": body, truncated = formatLSPStep(evt, maxBytes)`。`formatLSPStep`：正文 = 工具返回的文本（`evt.Output` 中没有额外字段时就用结果文本；参考 `formatRetrieveOutputStep` 取结果文本的写法），包在 ```` ```text ```` 代码块里，用 `clampBody` 限长。`SummarizeToolStep` 中 `lsp` 完成时的摘要：`looked up <operation> · <result_count> result(s)`。
- `pkg/tui/render.go`：
  - `failedActionPhrase` 加 `case "lsp": return "look up"`。
  - `toolDisplayParts` 加 `case lower == "lsp":` 分支：运行中 `action = "Looking up"`，完成 `action = "Looked up"`，失败 `"Failed to look up"`；`target = inputString(meta, "operation")`，再拼 ` · ` + `inputString(meta, "symbol")`（非空时）或 `inputString(meta, "query")`（非空时）或 `file_path:line`（`inputString(meta, "file_path")` 用与 `read_file` 分支相同的路径缩短方式）。

**验证**：`CGO_ENABLED=1 go test -tags fts5 ./pkg/tool ./pkg/tui -count=1` → `ok`。

### 步骤 6：写测试

见「测试计划」。

### 步骤 7：全量检查

**验证**：`gofmt -l cmd pkg third_party` 无输出；`go vet ./...` 退出码 0；`for d in tool run safety tui; do ls pkg/$d/*.go | grep -v _test.go | wc -l; done` 四个都仍是 20；`scripts/package-graph.sh` 后提交 `graph.json`；`CGO_ENABLED=1 go test -tags fts5 ./... -count=1 -timeout 20m` → 全部 `ok`。

## 测试计划

- `pkg/tool/search_test.go`（`package tool`，用一个记录调用的 `CodeIntelligence` 测试桩）
  - `TestLSPToolDescriptionIsAppendixB1`：`lspToolDescription` 与规范附录 B.1 的文本逐字相等（把文本写成测试里的常量）。
  - `TestLSPToolSchemaGolden`：`json.Marshal(tool.InputSchema())` 与写在测试里的期望 JSON 相等（任何字段、枚举、描述变化都会失败；这是 prompt 前缀稳定性的守门测试）。
  - `TestLSPToolPassesQuery`：参数 → 桩收到的 `CodeIntelQuery` 字段正确（`AbsPath` 是解析后的绝对路径、`MaxResults` 默认 50 / 上限 200、`DisplayPath` 为原始参数）；返回文本原样；`CaptureToolOutput` 的字段存在。
  - `TestLSPToolAuthorizesLikeReadFile`：可读根之外的路径 → 与 `read_file` 相同的 `ErrPathNotAllowed`；`ReadPathDenied` 的路径 → `ErrPathReadDenied`；桩没有被调用。
  - `TestLSPToolPreviewAllowed`：根内路径 true、根外 false、被 deny 的 false。
- `pkg/tool/registry_test.go`
  - `TestLSPToolRegisteredOnlyWhenFrozenOn`：`CodeIntel == nil` → 没有 `lsp`；`CodeIntel` 非 nil 但 `CodeIntelTool == false` → 没有；都满足 → 有，且 `ToolMetaByName("lsp")` 为 `ReadOnly`、`ConcurrencySafe`、category `code`。
  - `TestLSPToolRegistrationOrder`：注册顺序中 `lsp` 紧跟在 `request_permissions`（未启用时为 `web_search`）之后。
- `pkg/tool/file_tools_test.go`：`TestAuthorizeReadMatchesReadFile`（同一组路径对两个工具名得到相同的错误类别；受保护路径时 `RequiresActionError.ToolName` 等于传入的工具名）。
- `pkg/run/permissions_test.go`：`TestLSPEvaluatedAsRead`：配置 `Read(<dir>/**)` deny 规则 → 带该目录下 `file_path` 的 `lsp` 调用被拒；无 `file_path` 的 `workspace_symbols` → 允许（`LSP` 默认只读放行）。
- `pkg/safety/permission_rules_test.go`：`CanonicalToolName("lsp") == "LSP"`；`pkg/safety/engine_test.go`：`IsSafeReadOnlyTool("LSP")` 为 true。
- `pkg/tool/format_test.go`：`TestFormatLSPStep`：完成的步骤正文为代码块包裹的结果文本。
- `pkg/tui/render_test.go`：`TestToolDisplayPartsLSP`：运行中 / 完成 / 失败三种 `action`，`target` 的三种拼法。
- **prompt 前缀稳定性**（`pkg/run`，写在 `runner_test.go`）：`TestLSPToolTableStableAcrossServerChanges`：用一个 `CodeIntelTool: true` 的测试桩 Deps 加载 Runner，序列化 `LoadedTools()`（名称、描述、schema）；对桩执行 `SetEnabled`、模拟 `Reconcile`（调用 `r.LoadConfig` 同一配置的副本）后再序列化，两次字节相同。构建方式照 `pkg/run/config_test.go`（约 :160–195）：`cfg` 里配一个指向 `http://127.0.0.1:9/v1` 的 openai 提供商，`r := &Runner{Deps: &Deps{Home: t.TempDir(), AppCfg: cfg, CodeIntel: stub, CodeIntelControl: stub, CodeIntelTool: true}}`，`r.Load()`，再遍历 `r.LoadedTools()`。

## 完成判据

- [ ] 上述测试全部通过；`TestLSPToolSchemaGolden` 与 `TestLSPToolDescriptionIsAppendixB1` 存在
- [ ] `grep -n "lsp" pkg/safety/permission_rules.go pkg/safety/engine.go` 各至少一行
- [ ] 四个满额包的生产文件数仍为 20
- [ ] 系统提示未改动：`git diff --stat` 中没有 `pkg/agent`、`pkg/run/orchestration_llm.go`、`pkg/run/config.go`
- [ ] `graph.json` 已提交；全量测试通过
- [ ] README 中任务 10 状态为 `DONE`

## STOP 条件

- 抽出 `authorizeRead` 后任何既有 `read_file` 测试的结果改变。
- `invopop/jsonschema` 生成的 schema 与标签不符（例如枚举丢失），需要改工具构造方式。
- 需要在满额包里新建文件，或修改系统提示。

## 维护说明

- `lspToolDescription` 与 schema 是 prompt 前缀的一部分：任何修改都会让所有启用了 LSP 的会话缓存失效，必须改规范附录 B 并在 PR 里说明。
- 权限：带文件的调用按 `Read` 评估是规范 §15 决策 9；不要新增 `LSP(path)` 规则语法。
- 评审重点：`authorizeRead` 的搬移是否逐行等价；注册顺序；nil 检查。
