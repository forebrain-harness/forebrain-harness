# 任务 02：定义 code intelligence 的 port 接口与 DTO

> **执行者须知**：逐步执行；每一步运行「验证」命令并确认结果后再进入下一步。出现「STOP 条件」中的任何情况立即停止并汇报。完成后把 `docs/plan/lsp/README.md` 中本任务状态改为 `DONE`。
>
> **规范**：`docs/plan/LSP_CODE_INTELLIGENCE_PLAN.md`。开始前读规范 §3.3（分层约束）、§4.2（port 与 DTO）、§8.3.5、§10。
>
> **漂移检查（先运行）**：`git diff --stat 6305ea9..HEAD -- pkg/event pkg/tool/search.go pkg/tool/registry.go pkg/architecture/cache_test.go`。有输出时对比「现状」摘录，对不上即 STOP。

## 状态

- 优先级：P0 · 工作量：S · 风险：LOW
- 依赖：无
- 类别：direction
- 计划基于：`6305ea9`，2026-09-29

## 为什么

`pkg/tool`（fan-out 8/8）、`pkg/run`（14/14）、`pkg/tui`（19/19）、`pkg/gateway`（18/18）都不能再新增包导入，所以它们不能直接依赖新包 `pkg/lsp`。按仓库既定做法——「消费方定义接口，组合根注入」——把工具与界面需要的能力定义成 `pkg/tool` 里的接口，把跨层传递的数据定义成 `pkg/event` 里的纯数据结构。`pkg/tool` 已有 20 个生产文件（上限），**不能新建文件**，接口追加到 `pkg/tool/search.go`（「代码搜索」主题，之后任务 10 的 `lsp` 工具也放这里）。本任务不改任何行为，也不改包之间的 import 关系。

## 现状

- `pkg/architecture/cache_test.go:351-358` 的分层表：`tool`、`event` 都在 Layer 2；`sameLayerEdges`（:526 起）里已有 `"tool": {"event", "memory", "safety"}`，所以 `tool` 使用 `event` 的类型不产生新边。
- `pkg/event/tools.go:7-25` 是 `event` 包里纯数据结构的写法范例（`ToolMeta`，带 json tag，无方法）。
- `pkg/event` 目前只导入 `pkg/safety`；**本任务不得让 `event` 导入 `tool`**（`TestLowerLayersDoNotImportSurfaces` 等测试会失败，且违反分层）。
- `pkg/tool/registry.go:264-278` 的 `AgentToolRuntime` 是工具运行时依赖的注入点（任务 03 才给它加字段，本任务不动）。
- `pkg/tool/search.go:1` 的文件头是 `// Text search and ranking used by the semantic tools.`，文件目前只导入标准库；`pkg/tool` 里没有 `search_test.go`。
- 结构约束（`pkg/architecture/cache_test.go`）：每包最多 20 个生产文件（`pkg/tool` 已 20 个，`pkg/event` 16 个）；`X_test.go` 必须对应 `X.go`；import 分组标准库在前。

## 范围

**只新建 / 修改：**

- `pkg/event/lsp.go`、`pkg/event/lsp_test.go`（新建）
- `pkg/tool/search.go`（在文件末尾追加；改文件头注释）
- `pkg/architecture/testdata/graph.json`（重新生成）
- `pkg/tool/search_test.go`（新建）

**不要碰：** 其他所有文件。尤其不要在 `pkg/tool` 新建生产文件，不要现在就改 `AgentToolRuntime` 或注册任何工具。

## 步骤

### 步骤 1：新建 `pkg/event/lsp.go`

内容如下（类型名、字段名、json tag、常量值必须完全一致）：

```go
// Language-server data shared by the lsp runtime, the tools and the surfaces.
// Pure data: the behavior lives in pkg/lsp behind the ports in pkg/tool.
package event

// RunEventLSPRecommendation is the run event that carries an LSPRecommendation
// to the session's surfaces.
const RunEventLSPRecommendation = "lsp_recommendation"

// LSPServerState is one server's state as the surfaces show it.
type LSPServerState string

const (
	LSPStateNotInstalled LSPServerState = "not_installed"
	LSPStateAvailable    LSPServerState = "available" // installed, not enabled
	LSPStateBlocked      LSPServerState = "blocked"   // unusable configuration or project entry awaiting consent; see Note
	LSPStateStopped      LSPServerState = "stopped"   // enabled, no instance running
	LSPStateStarting     LSPServerState = "starting"
	LSPStateIndexing     LSPServerState = "indexing"
	LSPStateReady        LSPServerState = "ready"
	LSPStateFailed       LSPServerState = "failed"
)

// LSPServerStatus is one configured server in an LSPSnapshot.
type LSPServerStatus struct {
	ID              string         `json:"id"`
	DisplayName     string         `json:"display_name"`
	Languages       []string       `json:"languages,omitempty"`
	Role            string         `json:"role"`
	Scope           string         `json:"scope"` // "catalog" | "global" | "project"
	Enabled         bool           `json:"enabled"`
	State           LSPServerState `json:"state"`
	Command         string         `json:"command,omitempty"`
	BinaryPath      string         `json:"binary_path,omitempty"`
	Version         string         `json:"version,omitempty"`
	InstallCommand  string         `json:"install_command,omitempty"`
	Roots           []string       `json:"roots,omitempty"`
	PIDs            []int          `json:"pids,omitempty"`
	OpenDocuments   int            `json:"open_documents,omitempty"`
	Errors          int            `json:"errors,omitempty"`
	Warnings        int            `json:"warnings,omitempty"`
	IndexingPercent int            `json:"indexing_percent,omitempty"`
	LastError       string         `json:"last_error,omitempty"`
	LogPath         string         `json:"log_path,omitempty"`
	Note            string         `json:"note,omitempty"`
	ProjectWrites   []string       `json:"project_writes,omitempty"`
	// An install started from any surface: running, its last output lines,
	// and the failure text once it failed.
	Installing   bool     `json:"installing,omitempty"`
	InstallLog   []string `json:"install_log,omitempty"`
	InstallError string   `json:"install_error,omitempty"`
}

// LSPSnapshot is everything /lsp shows for one runner.
type LSPSnapshot struct {
	ProjectRoot                   string            `json:"project_root,omitempty"`
	Trusted                       bool              `json:"trusted"`
	FeatureEnabled                bool              `json:"feature_enabled"`
	ToolRegistered                bool              `json:"tool_registered"`
	RecommendationsDisabled       bool              `json:"recommendations_disabled"`
	RecommendationsDisabledReason string            `json:"recommendations_disabled_reason,omitempty"`
	// ProjectNotes says why .forebrain/lsp_servers.yaml, or an entry of it,
	// was ignored.
	ProjectNotes []string          `json:"project_notes,omitempty"`
	Servers      []LSPServerStatus `json:"servers"`
}

// LSPRecommendation offers to enable (or install and enable) one server.
type LSPRecommendation struct {
	ID               string   `json:"id"`
	ServerID         string   `json:"server_id"`
	DisplayName      string   `json:"display_name"`
	Languages        []string `json:"languages,omitempty"`
	TriggerExtension string   `json:"trigger_extension"`
	Mode             string   `json:"mode"` // "enable" | "install"
	BinaryPath       string   `json:"binary_path,omitempty"`
	Version          string   `json:"version,omitempty"`
	InstallCommand   string   `json:"install_command,omitempty"`
}

// LSPRecommendationChoice is the user's answer to an LSPRecommendation.
type LSPRecommendationChoice string

const (
	LSPChoiceEnable     LSPRecommendationChoice = "enable"
	LSPChoiceInstall    LSPRecommendationChoice = "install"
	LSPChoiceNotNow     LSPRecommendationChoice = "not_now"
	LSPChoiceNever      LSPRecommendationChoice = "never"
	LSPChoiceDisableAll LSPRecommendationChoice = "disable_all"
)

// Valid reports whether c is one of the five choices.
func (c LSPRecommendationChoice) Valid() bool {
	switch c {
	case LSPChoiceEnable, LSPChoiceInstall, LSPChoiceNotNow, LSPChoiceNever, LSPChoiceDisableAll:
		return true
	}
	return false
}

// LSPDiagnostic is one problem, positioned for people: 1-based line and
// column, path relative to the project root with forward slashes.
type LSPDiagnostic struct {
	Path     string `json:"path"`
	Line     int    `json:"line"`
	Column   int    `json:"column"`
	Severity string `json:"severity"` // "error" | "warning" | "info" | "hint"
	Source   string `json:"source,omitempty"`
	Code     string `json:"code,omitempty"`
	Message  string `json:"message"`
}

// LSPDiagnosticsSummary is what an edit tool reports under "lsp_diagnostics".
type LSPDiagnosticsSummary struct {
	New                 int             `json:"new"`
	Files               int             `json:"files"`
	Servers             []string        `json:"servers,omitempty"`
	Items               []LSPDiagnostic `json:"items,omitempty"`
	PendingFiles        []string        `json:"pending_files,omitempty"`
	BaselineUnavailable bool            `json:"baseline_unavailable,omitempty"`
}
```

**验证**：`CGO_ENABLED=1 go build -tags fts5 ./pkg/event` → 退出码 0。

### 步骤 2：在 `pkg/tool/search.go` 追加 port

1. 把文件头第一行改为：

```go
// Code search: text search and ranking used by the semantic tools, and the
// code intelligence ports the language-server runtime implements (the lsp
// tool and the edit tools' diagnostics go through them).
```

2. 在 import 里加入 `"context"`（标准库组）与 `"github.com/forebrain-harness/forebrain-harness/pkg/event"`（第二组，空行分隔）。
3. 在文件**末尾**追加下列代码（pkg/lsp 实现这些接口、pkg/process 注入，所以本包与界面都不导入 pkg/lsp）：

```go

// Operations of the lsp tool, in the order its schema lists them.
const (
	LSPOpDefinition       = "definition"
	LSPOpDeclaration      = "declaration"
	LSPOpTypeDefinition   = "type_definition"
	LSPOpImplementation   = "implementation"
	LSPOpReferences       = "references"
	LSPOpHover            = "hover"
	LSPOpDocumentSymbols  = "document_symbols"
	LSPOpWorkspaceSymbols = "workspace_symbols"
	LSPOpIncomingCalls    = "incoming_calls"
	LSPOpOutgoingCalls    = "outgoing_calls"
	LSPOpSupertypes       = "supertypes"
	LSPOpSubtypes         = "subtypes"
	LSPOpDiagnostics      = "diagnostics"
)

// LSPOperations lists every operation in schema order.
var LSPOperations = []string{
	LSPOpDefinition, LSPOpDeclaration, LSPOpTypeDefinition, LSPOpImplementation,
	LSPOpReferences, LSPOpHover, LSPOpDocumentSymbols, LSPOpWorkspaceSymbols,
	LSPOpIncomingCalls, LSPOpOutgoingCalls, LSPOpSupertypes, LSPOpSubtypes,
	LSPOpDiagnostics,
}

// CodeIntelQuery is one lsp tool call after the tool validated and authorized it.
type CodeIntelQuery struct {
	Operation          string
	AbsPath            string // resolved and read-authorized; empty when the operation allows it
	DisplayPath        string // what the model passed, for messages
	Line               int    // 1-based; 0 when not applicable
	Column             int    // 1-based characters; 0 when Symbol is used or not applicable
	Symbol             string
	Query              string
	IncludeDeclaration bool
	MaxResults         int // already defaulted (50) and capped (200) by the tool
	// PreviewAllowed reports whether a result location may carry a source
	// preview: true only for paths the session may read without asking.
	PreviewAllowed func(absPath string) bool
}

// CodeIntelResult is the text the model receives plus the fields the
// surfaces render.
type CodeIntelResult struct {
	Text    string
	Display map[string]any
}

// FileChange is one file an edit tool wrote. Before is nil for a new file;
// After is nil for a deleted file.
type FileChange struct {
	AbsPath string
	Before  []byte
	After   []byte
}

// DiagnosticsDelta is what an edit reports: Text is the <diagnostics> block
// appended to the tool result ("" when there is nothing to say).
type DiagnosticsDelta struct {
	Text    string
	Summary event.LSPDiagnosticsSummary
}

// Empty reports whether the edit has nothing to report.
func (d DiagnosticsDelta) Empty() bool { return d.Text == "" }

// CodeIntelligence is what the tools use. Every method must be safe for
// concurrent use and must never start a server on the Runner.Load path.
type CodeIntelligence interface {
	// Handles reports, without starting anything, whether an enabled server
	// covers absPath in this project.
	Handles(absPath string) bool
	// Query runs one lsp tool operation, starting the server if needed.
	Query(ctx context.Context, q CodeIntelQuery) (CodeIntelResult, error)
	// DidWrite syncs files an edit tool just wrote and waits, at most the
	// diagnostics wait window, for the problems the edit introduced. It never
	// fails the edit: problems are logged and reported as an empty delta.
	// agentSessionID (llm.AgentSessionIDFromContext at the call site) keys the
	// late-diagnostics queue that PeekLate reads.
	DidWrite(ctx context.Context, agentSessionID string, changes []FileChange) DiagnosticsDelta
	// DidRead opens absPath on a server that is already running, as a
	// diagnostics baseline. It never starts a server.
	DidRead(ctx context.Context, absPath string, content []byte)
	// DidRunShell tells the runtime a shell command finished, so files changed
	// outside the edit tools are re-synced. It returns immediately.
	DidRunShell(ctx context.Context)
	// PeekLate returns the late-diagnostics reminder text pending for one agent
	// session ("" when none) and a token for AckLate.
	PeekLate(agentSessionID string) (text string, token uint64)
	// AckLate marks everything up to token as delivered.
	AckLate(agentSessionID string, token uint64)
}

// CodeIntelControl is what the surfaces use (/lsp, recommendations, web API).
type CodeIntelControl interface {
	Snapshot() event.LSPSnapshot
	// Subscribe calls fn whenever the snapshot changes; cancel stops it.
	Subscribe(fn func(event.LSPSnapshot)) (cancel func())
	SetEnabled(serverID string, enabled bool) error
	Restart(serverID string) error
	// Install runs the server's install recipe; progress receives output lines.
	Install(ctx context.Context, serverID string, progress func(line string)) error
	// SetRecommendationListener installs the callback that publishes a
	// recommendation to the session named by ctx. Nil removes it.
	SetRecommendationListener(fn func(ctx context.Context, rec event.LSPRecommendation))
	// DecideRecommendation applies the user's answer. A recommendation the
	// runtime did not make, or one already answered, returns
	// ErrUnknownLSPRecommendation.
	DecideRecommendation(recommendationID string, choice event.LSPRecommendationChoice) error
	ResetRecommendations() error
}

// ErrUnknownLSPRecommendation is DecideRecommendation's answer for an id it
// does not know. It lives beside the port so surfaces can test for it without
// importing the runtime.
var ErrUnknownLSPRecommendation = errors.New("unknown or already answered language server recommendation")
```

（import 里同时加入 `"errors"`。）

**验证**：`CGO_ENABLED=1 go build -tags fts5 ./pkg/tool` → 退出码 0。

### 步骤 3：写测试

- `pkg/event/lsp_test.go`：
  - `TestLSPRecommendationChoiceValid`：五个常量为 true，`""` 与 `"yes"` 为 false。
  - `TestLSPSnapshotJSONShape`：构造一个含一个服务器的 `LSPSnapshot`，`json.Marshal` 后断言包含 `"feature_enabled":`、`"servers":[`、`"state":"ready"`，且空的可选字段（如 `binary_path`）不出现。
- `pkg/tool/search_test.go`（新建，`package tool`）：
  - `TestLSPOperationsListsEveryOperationOnce`：`LSPOperations` 长度 13、无重复、第一个是 `definition`、最后一个是 `diagnostics`。
  - `TestDiagnosticsDeltaEmpty`：零值 `Empty()` 为 true；`Text` 非空为 false。
  - 编译期断言：在测试文件里定义 `type nopCodeIntel struct{}` 实现两个接口的全部方法（空实现），并写 `var _ CodeIntelligence = nopCodeIntel{}`、`var _ CodeIntelControl = nopCodeIntel{}`。

**验证**：`CGO_ENABLED=1 go test -tags fts5 ./pkg/event ./pkg/tool -run 'LSP|DiagnosticsDelta' -count=1 -v` → 全部 PASS。

### 步骤 4：确认没有改变包关系

**验证**：

- `CGO_ENABLED=1 go test -tags fts5 ./pkg/architecture -count=1` → `ok`
- `scripts/package-graph.sh && git diff pkg/architecture/testdata/graph.json` → 只有 `pkg/event`、`pkg/tool` 的 `loc` 数值变化，**没有**新增或删除的 `edges`、`fan_out`/`fan_in` 不变（否则按 STOP 处理）；提交这个文件
- `go vet ./pkg/event ./pkg/tool` → 退出码 0；`gofmt -l pkg/event pkg/tool` → 无输出

## 测试计划

见步骤 3。风格照 `pkg/event/*_test.go`，只用标准库。

## 完成判据

- [ ] `git status --short` 只列出 `pkg/event/lsp.go`、`pkg/event/lsp_test.go`、`pkg/tool/search.go`、`pkg/tool/search_test.go`
- [ ] `ls pkg/tool/*.go | grep -v _test.go | wc -l` 仍为 20
- [ ] `CGO_ENABLED=1 go test -tags fts5 ./pkg/event ./pkg/tool ./pkg/architecture -count=1` 全部 `ok`
- [ ] `graph.json` 已重新生成并提交，且只有 `loc` 变化
- [ ] `grep -n "RunEventLSPRecommendation = \"lsp_recommendation\"" pkg/event/lsp.go` 有一行
- [ ] README 中任务 02 状态为 `DONE`

## STOP 条件

- 需要让 `pkg/event` 导入 `pkg/tool`，或让 `pkg/tool` 导入任何新包，或在 `pkg/tool` 新建生产文件。
- `graph.json` 出现 `loc` 以外的变化（新边或 fan-out 变化）。
- 「现状」描述的分层关系与代码不符。

## 维护说明

- 这两处是后续任务的契约：任务 03 建骨架实现并注入，任务 08/09/12/13 在 `pkg/lsp` 填实现，任务 10/11 在工具里调用，任务 13/14 在界面里调用。**改接口签名必须同时改所有实现与调用方**，并在 PR 里说明。
- 评审重点：接口方法的注释写清了「不启动服务器」「不让编辑失败」这两条行为约束。
