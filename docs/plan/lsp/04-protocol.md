# 任务 04：协议层——JSON-RPC 传输、协议类型、位置换算与 URI

> **执行者须知**：逐步执行；每一步运行「验证」命令并确认结果后再进入下一步。出现「STOP 条件」中的任何情况立即停止并汇报。完成后把 `docs/plan/lsp/README.md` 中本任务状态改为 `DONE`。
>
> **规范**：`docs/plan/LSP_CODE_INTELLIGENCE_PLAN.md`。开始前读规范 §3.14、§7.1、§8.2、附录 A、附录 B.6、附录 C（`line-out-of-range`、`symbol-not-found`）。
>
> **前置**：任务 03 已合入（`pkg/lsp/doc.go` 存在）。
>
> **漂移检查（先运行）**：`git diff --stat 6305ea9..HEAD -- pkg/lsp`（此时只应看到任务 03 新建的文件）。

## 状态

- 优先级：P0 · 工作量：M · 风险：LOW（纯库代码，无调用方）
- 依赖：03
- 类别：direction
- 计划基于：`6305ea9`，2026-09-29

## 为什么

后面所有与语言服务器的交互都走这一层：按 LSP 基础协议分帧的 JSON-RPC 2.0、LSP 3.17 的一个子集类型（含联合类型解码）、模型的 1-based 行列与 LSP 位置（UTF-8/16/32 代码单元）之间的换算、以及 file URI 与本地路径的互转。这些都是纯函数或纯 I/O，可以完全用单元测试覆盖。由于 `pkg/` 下不允许二级包（`TestPackageShape`），它们都是 `pkg/lsp` 下的文件，不是子包。

## 现状

- `pkg/lsp` 只有任务 03 的五个文件：`doc.go`、`pool.go`、`manager.go`、`control.go`、`resolve.go`。
- 仓库对 JSON 的写法：标准库 `encoding/json`；参见 `pkg/config/agent_llm.go:296-326` 的 `json.RawMessage` 风格处理。
- 不引入新的第三方依赖（规范 §15 决策 6）。

## 范围

**新建：** `pkg/lsp/jsonrpc.go`、`pkg/lsp/protocol.go`、`pkg/lsp/position.go`，以及同名 `_test.go`。**修改：** `pkg/lsp/doc.go`（文件清单一句）。

**不要碰：** `pkg/lsp` 以外的任何文件；`go.mod`（不加依赖）。

## 步骤

### 步骤 1：`pkg/lsp/jsonrpc.go` —— 分帧与连接

公开 API（名称、签名必须一致）：

```go
// MaxMessageBytes bounds one incoming message (spec §7.1).
const MaxMessageBytes = 64 << 20

// Standard JSON-RPC and LSP error codes used by this package.
const (
	CodeParseError       int64 = -32700
	CodeInvalidRequest   int64 = -32600
	CodeMethodNotFound   int64 = -32601
	CodeInvalidParams    int64 = -32602
	CodeInternalError    int64 = -32603
	CodeRequestCancelled int64 = -32800
)

// RPCError is a JSON-RPC error object; it is also the error type Call returns
// when the server answers with an error.
type RPCError struct {
	Code    int64           `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func (e *RPCError) Error() string // "jsonrpc error <code>: <message>"

// ErrMethodNotFound, returned by a RequestHandler, answers -32601.
var ErrMethodNotFound = errors.New("method not found")

// ErrMessageTooLarge is returned by ReadMessage for a body over MaxMessageBytes.
var ErrMessageTooLarge = errors.New("jsonrpc: message too large")

// ReadMessage reads one framed message body.
func ReadMessage(r *bufio.Reader) ([]byte, error)

// WriteMessage writes one framed message.
func WriteMessage(w io.Writer, body []byte) error

// RequestHandler answers a server-to-client request. Returning an *RPCError
// sends it as-is; ErrMethodNotFound sends -32601; any other error sends -32603
// with err.Error() as the message.
type RequestHandler func(ctx context.Context, method string, params json.RawMessage) (any, error)

// NotificationHandler receives server-to-client notifications, in arrival order.
type NotificationHandler func(method string, params json.RawMessage)

type ConnOptions struct {
	OnRequest RequestHandler      // nil: every request answers -32601
	OnNotify  NotificationHandler // nil: notifications are dropped
}

// Conn is one JSON-RPC connection. It starts its read loop in NewConn.
type Conn struct { /* unexported fields */ }

func NewConn(r io.Reader, w io.Writer, opts ConnOptions) *Conn

// Call sends a request and decodes the result into result (may be nil).
// On ctx cancellation it sends $/cancelRequest {"id": <id>} and returns ctx.Err().
func (c *Conn) Call(ctx context.Context, method string, params, result any) error

// Notify sends a notification.
func (c *Conn) Notify(method string, params any) error

// Close stops the connection; pending calls return ErrConnClosed. Idempotent.
func (c *Conn) Close() error

// Done is closed when the read loop ends (peer closed, protocol error, Close).
func (c *Conn) Done() <-chan struct{}

// Err reports why the read loop ended (nil after Close).
func (c *Conn) Err() error

var ErrConnClosed = errors.New("jsonrpc: connection closed")
```

行为规则：

1. **读分帧**：逐行读头（以 `\r\n` 结尾，也容忍单独的 `\n`），头名大小写不敏感；只认 `Content-Length`，其余头忽略；空行结束头部；缺 `Content-Length` 或不是非负整数 → 返回错误；值 > `MaxMessageBytes` → `ErrMessageTooLarge`；然后 `io.ReadFull` 读正文。
2. **写分帧**：`Content-Length: <n>\r\n\r\n<body>`，一次 `Write` 写头、一次写体，全程持有写锁。
3. **消息分类**（读循环）：解码到 `{jsonrpc, id, method, params, result, error}`（`id` 用 `json.RawMessage`）；有 `method` 且有 `id` → 服务器请求；有 `method` 无 `id` → 通知；无 `method` 有 `id` → 响应；否则忽略。
4. **响应匹配**：请求 id 是递增 `int64`；响应的 id 解码为 `int64` 查待决表；找不到（已取消）就丢弃。
5. **服务器请求**：每个请求新起 goroutine 调 `OnRequest`，结果编码为 `result`（nil → JSON `null`），错误按 `RequestHandler` 注释转换；handler panic 要 recover 并回 -32603。回应的 `id` 原样使用请求的原始 JSON（可能是字符串）。
6. **通知**：在读循环里**同步**调用 `OnNotify`，保证顺序（handler 不得阻塞；调用方负责）。
7. **读循环退出**（EOF、协议错误、超大消息）：记录 `Err()`，关闭 `Done()`，所有待决 `Call` 返回 `ErrConnClosed`（包装原因）。
8. `Call` 的 `params == nil` 时不写 `params` 字段；`result` 为 nil 时忽略返回值。

**验证**：`CGO_ENABLED=1 go build -tags fts5 ./pkg/lsp` → 退出码 0。

### 步骤 2：`pkg/lsp/protocol.go` —— LSP 类型子集

全部用 LSP 的 camelCase json tag。需要的类型（名称必须一致，可按需加字段，不得省略下列字段）：

```go
type Position struct{ Line uint32 `json:"line"`; Character uint32 `json:"character"` }
type Range struct{ Start Position `json:"start"`; End Position `json:"end"` }
type Location struct{ URI string `json:"uri"`; Range Range `json:"range"` }
type LocationLink struct {
	OriginSelectionRange *Range `json:"originSelectionRange,omitempty"`
	TargetURI            string `json:"targetUri"`
	TargetRange          Range  `json:"targetRange"`
	TargetSelectionRange Range  `json:"targetSelectionRange"`
}
type TextDocumentIdentifier struct{ URI string `json:"uri"` }
type VersionedTextDocumentIdentifier struct{ URI string `json:"uri"`; Version int32 `json:"version"` }
type TextDocumentItem struct {
	URI string `json:"uri"`; LanguageID string `json:"languageId"`; Version int32 `json:"version"`; Text string `json:"text"`
}
type TextDocumentPositionParams struct {
	TextDocument TextDocumentIdentifier `json:"textDocument"`; Position Position `json:"position"`
}
type ReferenceParams struct {
	TextDocumentPositionParams
	Context struct{ IncludeDeclaration bool `json:"includeDeclaration"` } `json:"context"`
}
type Diagnostic struct {
	Range    Range           `json:"range"`
	Severity int             `json:"severity,omitempty"` // 1 error, 2 warning, 3 information, 4 hint; 0 = unspecified (treated as error)
	Code     json.RawMessage `json:"code,omitempty"`     // number or string; see DiagnosticCode
	Source   string          `json:"source,omitempty"`
	Message  string          `json:"message"`
}
type PublishDiagnosticsParams struct {
	URI string `json:"uri"`; Version *int32 `json:"version,omitempty"`; Diagnostics []Diagnostic `json:"diagnostics"`
}
type DocumentDiagnosticReport struct {
	Kind             string                              `json:"kind"` // "full" | "unchanged"
	ResultID         string                              `json:"resultId,omitempty"`
	Items            []Diagnostic                        `json:"items,omitempty"`
	RelatedDocuments map[string]DocumentDiagnosticReport `json:"relatedDocuments,omitempty"`
}
type DocumentSymbol struct {
	Name string `json:"name"`; Detail string `json:"detail,omitempty"`; Kind int `json:"kind"`
	Range Range `json:"range"`; SelectionRange Range `json:"selectionRange"`; Children []DocumentSymbol `json:"children,omitempty"`
}
type SymbolInformation struct {
	Name string `json:"name"`; Kind int `json:"kind"`; Location Location `json:"location"`; ContainerName string `json:"containerName,omitempty"`
}
type WorkspaceSymbol struct {
	Name string `json:"name"`; Kind int `json:"kind"`; ContainerName string `json:"containerName,omitempty"`
	Location json.RawMessage `json:"location"` // Location, or {"uri": ...} without a range
}
type CallHierarchyItem struct {
	Name string `json:"name"`; Kind int `json:"kind"`; Detail string `json:"detail,omitempty"`
	URI string `json:"uri"`; Range Range `json:"range"`; SelectionRange Range `json:"selectionRange"`; Data json.RawMessage `json:"data,omitempty"`
}
type CallHierarchyIncomingCall struct{ From CallHierarchyItem `json:"from"`; FromRanges []Range `json:"fromRanges"` }
type CallHierarchyOutgoingCall struct{ To CallHierarchyItem `json:"to"`; FromRanges []Range `json:"fromRanges"` }
type TypeHierarchyItem = CallHierarchyItem // same shape
type WorkspaceFolder struct{ URI string `json:"uri"`; Name string `json:"name"` }
type ProgressParams struct{ Token json.RawMessage `json:"token"`; Value json.RawMessage `json:"value"` }
type WorkDoneProgressValue struct {
	Kind string `json:"kind"`; Title string `json:"title,omitempty"`; Message string `json:"message,omitempty"`; Percentage *int `json:"percentage,omitempty"`
}
type MessageActionItem struct{ Title string `json:"title"` }
type ShowMessageRequestParams struct {
	Type int `json:"type"`; Message string `json:"message"`; Actions []MessageActionItem `json:"actions,omitempty"`
}
type ConfigurationItem struct{ ScopeURI string `json:"scopeUri,omitempty"`; Section string `json:"section,omitempty"` }
type ConfigurationParams struct{ Items []ConfigurationItem `json:"items"` }
type Registration struct{ ID string `json:"id"`; Method string `json:"method"`; RegisterOptions json.RawMessage `json:"registerOptions,omitempty"` }
type RegistrationParams struct{ Registrations []Registration `json:"registrations"` }
type FileEvent struct{ URI string `json:"uri"`; Type int `json:"type"` } // 1 created, 2 changed, 3 deleted

// ServerCapabilities keeps the raw capability object (spec §7.2).
type ServerCapabilities map[string]json.RawMessage
```

辅助函数（必须实现并测试）：

- `func (c ServerCapabilities) Supports(name string) bool`：键存在且值不是 `null`、`false`。
- `func (c ServerCapabilities) PositionEncoding() string`：`positionEncoding` 字符串，缺省 `"utf-16"`。
- `func (c ServerCapabilities) SaveIncludesText() bool`：`textDocumentSync` 为对象且其 `save` 为对象且 `includeText == true`。
- `func (c ServerCapabilities) WorkspaceFolderChanges() bool`：`workspace.workspaceFolders.changeNotifications` 为 `true` 或非空字符串。
- `func DecodeLocations(raw json.RawMessage) ([]Location, error)`：接受 `null`、单个 `Location`、`[]Location`、`[]LocationLink`（用 `TargetURI` + `TargetSelectionRange`）。
- `func DecodeHover(raw json.RawMessage) (string, error)`：接受 `null`（返回空串）与 `{contents, range}`；`contents` 可为 `MarkupContent{kind,value}`（取 value）、字符串、`{language, value}`（渲染为 ```` ```<language>\n<value>\n``` ````）或它们的数组（各项用 `\n\n` 连接）。
- `func DecodeDocumentSymbols(raw json.RawMessage) ([]DocumentSymbol, []SymbolInformation, error)`：数组第一个元素有 `location` 键则按 `SymbolInformation` 解，否则按 `DocumentSymbol` 解；`null` 两者都返回 nil。
- `func DiagnosticCode(raw json.RawMessage) string`：数字转十进制字符串，字符串去引号，其他返回空串。
- `func SeverityName(sev int) string`：1/0 → `"error"`，2 → `"warning"`，3 → `"info"`，4 → `"hint"`。
- `func SymbolKindName(kind int) string`：1–26 依次为 `file module namespace package class method property field constructor enum interface function variable constant string number boolean array object key null enummember struct event operator typeparameter`，其他为 `"symbol"`。

**验证**：`CGO_ENABLED=1 go build -tags fts5 ./pkg/lsp` → 退出码 0。

### 步骤 3：`pkg/lsp/position.go` —— 位置、符号锚定与 URI

```go
// Encoding is a negotiated LSP position encoding.
type Encoding string

const (
	EncodingUTF8  Encoding = "utf-8"
	EncodingUTF16 Encoding = "utf-16"
	EncodingUTF32 Encoding = "utf-32"
)

// ParseEncoding maps a capability value to an Encoding; unknown or empty is UTF-16.
func ParseEncoding(s string) Encoding

// LineOutOfRangeError: the requested 1-based line is past the end.
type LineOutOfRangeError struct{ Line, Lines int }
func (e *LineOutOfRangeError) Error() string // "line %d is past the end of the file (%d lines)"

// SymbolNotFoundError: AnchorSymbol found no match within ±3 lines.
type SymbolNotFoundError struct{ Symbol string; Line, From, To int }
func (e *SymbolNotFoundError) Error() string // `symbol "%s" not found on line %d (searched lines %d-%d)`

// LineText returns 1-based line n without its line terminator.
func LineText(content []byte, n int) (string, bool)

// LineCount counts lines the way read_file numbers them (a trailing newline does not start a new line).
func LineCount(content []byte) int

// ToLSPPosition converts a 1-based line and 1-based character column (spec §8.2).
// clamped reports that column was past the end of the line.
func ToLSPPosition(content []byte, line, column int, enc Encoding) (pos Position, clamped bool, err error)

// FromLSPPosition converts back to a 1-based line and 1-based character column.
// A position past the end of the content maps to line+1 / character+1 unchanged.
func FromLSPPosition(content []byte, pos Position, enc Encoding) (line, column int)

// AnchorSymbol finds symbol on 1-based line (or within ±3 lines, spec §8.2) and
// returns the line it was found on and its 1-based column.
func AnchorSymbol(content []byte, line int, symbol string) (foundLine, column int, err error)

// PathToURI converts an absolute local path to a file URI.
func PathToURI(abs string) string

// URIToPath converts a file URI back to a local path.
func URIToPath(uri string) (string, error)

// SamePath compares two local paths after filepath.Clean, case-insensitively on darwin and windows.
func SamePath(a, b string) bool
```

实现要求：

- 行的切分：按 `\n` 切；每行去掉末尾一个 `\r`；文件开头的 UTF-8 BOM（`EF BB BF`）不计入第一行的列。
- 列按 Unicode 码点（`utf8.DecodeRune`）计；非法 UTF-8 字节每个算一个码点、UTF-16 长度 1、UTF-8 长度 1。
- `AnchorSymbol` 的规则逐条照规范 §8.2「symbol 锚定」；标识符字符判断用 `unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' || r == '$'`；搜索范围不超出 `[1, LineCount]`；错误里的 `From`/`To` 是实际搜索的首末行。
- URI：为了在任何平台上测试两种系统的行为，内部实现为 `pathToURI(abs, goos string)` 与 `uriToPath(uri, goos string)`，公开函数传 `runtime.GOOS`。
  - Unix：`file://` + 路径，路径中除 `A–Z a–z 0–9 - . _ ~ / :` 以外的字节按 `%XX`（大写十六进制）编码。
  - Windows：`C:\a b\x.go` → `file:///C:/a%20b/x.go`（反斜杠变 `/`，盘符大写，冒号不编码）；UNC `\\host\share\x` → `file://host/share/x`。
  - 解析：接受 `file://localhost/...`；百分号解码（含 `%3A` → `:`）；Windows 下去掉路径前导 `/`、盘符转大写、`/` 变 `\`；非 `file` scheme 返回错误。

**验证**：`CGO_ENABLED=1 go build -tags fts5 ./pkg/lsp` → 退出码 0。

### 步骤 4：更新 `doc.go` 文件清单

在 `doc.go` 的 `Files:` 句子里加上：`jsonrpc.go (framing and the JSON-RPC connection), protocol.go (the LSP 3.17 types this client uses), position.go (line/column and URI mapping)`。

### 步骤 5：写测试

见「测试计划」。

**验证**：`CGO_ENABLED=1 go test -tags fts5 ./pkg/lsp -count=1 -race` → `ok`（带 `-race`，因为连接是并发的）。

### 步骤 6：全量检查

**验证**：`gofmt -l pkg/lsp` → 无输出；`go vet ./pkg/lsp` → 退出码 0；`CGO_ENABLED=1 go test -tags fts5 ./pkg/architecture -count=1` → `ok`；`scripts/package-graph.sh && git status --short pkg/architecture/testdata/graph.json` → 可能出现 ` M`（`pkg/lsp` 的 LOC 变化会改 `graph.json`），若出现则提交。

## 测试计划

表驱动，只用标准库：

- `jsonrpc_test.go`
  - `TestReadMessageFraming`：正常帧；头名小写 `content-length`；多余头 `Content-Type`；只有 `\n` 的行尾；用 `testing/iotest.OneByteReader` 逐字节读；缺长度报错；长度非数字报错；超过 `MaxMessageBytes` 返回 `ErrMessageTooLarge`（用伪造的头，不要真分配 64 MiB）。
  - `TestConnCallAndNotify`：两对 `io.Pipe` 连两个 `Conn`，一端作服务器（`OnRequest` 回显 params），另一端 `Call` 得到结果；`Notify` 到达对端 `OnNotify`，顺序保持（连发 100 条）。
  - `TestConnConcurrentCalls`：50 个 goroutine 并发 `Call`，结果与请求一一对应。
  - `TestConnCancelSendsCancelRequest`：服务器端 handler 阻塞；客户端 ctx 取消后 `Call` 返回 `context.Canceled`，服务器端 `OnNotify` 收到 `$/cancelRequest` 且 `id` 与请求 id 相同。
  - `TestServerRequestErrors`：handler 返回 `ErrMethodNotFound` → 对端收到 -32601；返回 `*RPCError{Code: 1}` → 原样；返回普通错误 → -32603；handler panic → -32603 且连接仍可用；字符串 id 的请求原样回 id。
  - `TestConnCloseFailsPendingCalls`：对端不回应时 `Close`，`Call` 返回包含 `ErrConnClosed` 的错误；`Done()` 已关闭。
- `protocol_test.go`
  - `TestServerCapabilities`：`Supports` 对缺失、`null`、`false`、`true`、对象；`PositionEncoding` 缺省 utf-16；`SaveIncludesText`；`WorkspaceFolderChanges` 对 `true`、`"id"`、`false`、缺失。
  - `TestDecodeLocations`：null、单个、数组、LocationLink 数组。
  - `TestDecodeHover`：MarkupContent、字符串、`{language,value}`、混合数组、null。
  - `TestDecodeDocumentSymbols`：层级型与扁平型各一。
  - `TestDiagnosticCodeAndNames`：数字码、字符串码、缺失；`SeverityName`、`SymbolKindName` 边界。
- `position_test.go`
  - `TestToLSPPosition`：ASCII；中文（UTF-8 3 字节、UTF-16 1 单元）；emoji `😀`（UTF-8 4、UTF-16 2、UTF-32 1）；tab；CRLF 行；BOM；列超出（`clamped`）；行超出（`*LineOutOfRangeError`，`Lines` 正确）。
  - `TestFromLSPPositionRoundTrip`：上述每个位置 `To` 再 `From` 回到原值（未夹紧时）。
  - `TestAnchorSymbol`：本行命中；`Foo.bar` 取 `bar`；`pkg::name`、`obj->field`、`Class#method`；子串不算命中（在 `foobar` 里找 `foo` 失败）；上一行命中（返回实际行号）；±3 行外报 `*SymbolNotFoundError` 且 `From/To` 被文件边界截断。
  - `TestPathURI`：Unix `/home/a b/x#1.go` ↔ `file:///home/a%20b/x%231.go`；Windows `C:\Users\me\x.go` ↔ `file:///C:/Users/me/x.go`；`file:///c%3A/Users/me/x.go` 在 windows 规则下 → `C:\Users\me\x.go`；`file://localhost/tmp/x` → `/tmp/x`；`http://x` 报错。
  - `FuzzToLSPPosition`（`go test` 默认只跑种子语料）：任意内容与行列不 panic。

## 完成判据

- [ ] `ls pkg/lsp/*.go | grep -v _test.go | wc -l` 为 8
- [ ] `CGO_ENABLED=1 go test -tags fts5 ./pkg/lsp -count=1 -race` 通过
- [ ] `go vet ./pkg/lsp`、`gofmt -l pkg/lsp` 干净；`go.mod` 未变化
- [ ] `CGO_ENABLED=1 go test -tags fts5 ./pkg/architecture -count=1` 通过
- [ ] README 中任务 04 状态为 `DONE`

## STOP 条件

- 需要新增第三方依赖。
- 需要在 `pkg/lsp` 下建子目录包。
- `-race` 报告数据竞争且一次修复后仍存在。

## 维护说明

- 任务 05 用 `Conn` 驱动真实服务器；任务 07 用位置换算与 `PathToURI`；任务 09 用各 `Decode*` 函数与名称表。
- `OnNotify` 在读循环里同步调用：任务 05 的通知处理必须快速返回（只更新内存状态、不做 I/O）。
- 评审重点：分帧的边界情况、取消时是否清理待决表、URI 编码字符集。
