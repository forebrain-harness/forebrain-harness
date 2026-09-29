# 任务 07：文档同步与诊断存储（含「新引入」计算与诊断文本格式）

> **执行者须知**：逐步执行；每一步运行「验证」命令并确认结果后再进入下一步。出现「STOP 条件」中的任何情况立即停止并汇报。完成后把 `docs/plan/lsp/README.md` 中本任务状态改为 `DONE`。
>
> **规范**：`docs/plan/LSP_CODE_INTELLIGENCE_PLAN.md`。开始前读规范 §2（「基线」「安静期」）、§7.6、§8.2（输出换算）、§8.3.3–§8.3.5、§8.4、附录 B.3、B.4。
>
> **前置**：任务 05 已合入（`Instance`、`StartInstance`、`instance_test.go` 里的假服务器与 `startFake`）。
>
> **漂移检查（先运行）**：`git diff --stat 6305ea9..HEAD -- pkg/lsp`，确认 `instance.go` 的公开 API 与任务 05 计划一致（`OnNotification`、`OnRestart` 字段存在）。

## 状态

- 优先级：P0 · 工作量：M · 风险：LOW
- 依赖：05
- 类别：direction
- 计划基于：`6305ea9`，2026-09-29

## 为什么

两件事：① 让服务器看到的文件内容与磁盘一致（打开、变更、保存、关闭、重启后重开、LRU 上限）；② 保存服务器报出的诊断，并提供「等某个版本的诊断」「等安静期」「这次编辑新引入了哪些问题」以及把问题渲染成模型可见文本（附录 B.3/B.4）的能力。任务 08 的 `DidWrite` 与迟到提醒直接组合这些函数。

## 现状

- `pkg/lsp/protocol.go`（任务 04）已有 `Diagnostic`、`PublishDiagnosticsParams`、`DocumentDiagnosticReport`、`TextDocumentItem`、`VersionedTextDocumentIdentifier`、`SeverityName`、`DiagnosticCode`。**注意**：同一个包里已有名为 `Diagnostic` 的协议类型，本任务的内部类型叫 `Problem`。
- `pkg/lsp/position.go`（任务 04）有 `FromLSPPosition`、`PathToURI`、`URIToPath`。
- `pkg/lsp/instance.go`（任务 05）：`Instance.Notify`、`Call`、`Capabilities()`、`Encoding()`，`InstanceSpec.OnNotification` / `OnRestart`；实例内部有 `pullStale` 标记（`workspace/diagnostic/refresh` 置位）——若任务 05 没有导出读取方法，本任务在 `instance.go` 加一个 `func (i *Instance) consumePullStale() bool`（唯一允许修改 `instance.go` 的地方）。
- `event.LSPDiagnostic`、`event.LSPDiagnosticsSummary`（任务 02）。

## 范围

**新建：** `pkg/lsp/docsync.go`、`pkg/lsp/diagnostics.go`、`docsync_test.go`、`diagnostics_test.go`。**修改：** `pkg/lsp/doc.go`、（必要时）`pkg/lsp/instance.go` 的 `consumePullStale`、`pkg/architecture/testdata/graph.json`。

**不要碰：** `pool.go`、`manager.go`、`control.go`、`resolve.go`、`catalog.go`。

## 步骤

### 步骤 1：`docsync.go`

```go
// MaxDocumentBytes: larger files are never given to a server (spec §7.6).
const MaxDocumentBytes = 4 << 20

var (
	ErrFileTooLarge = errors.New("file is larger than 4 MiB")
	ErrNotText      = errors.New("file is not UTF-8 text")
)

// DocSync keeps one instance's open documents in step with the disk.
type DocSync struct { /* unexported: inst, languageFor, maxOpen, mu, docs map[string]*openDoc */ }

type openDoc struct {
	version  int32
	sum      [32]byte
	mtime    time.Time
	size     int64
	language string
	content  []byte
	lastUsed time.Time
}

// NewDocSync binds to one instance. languageFor maps an absolute path to its
// languageId; maxOpen <= 0 means 64.
func NewDocSync(inst *Instance, languageFor func(absPath string) string, maxOpen int) *DocSync

// EnsureSynced opens absPath from disk, or sends didChange if the disk
// differs from what the server has. Returns the synced version and content.
func (d *DocSync) EnsureSynced(ctx context.Context, absPath string) (int32, []byte, error)

// OpenWith opens absPath with the given content (a baseline before an edit);
// if already open it sends didChange with that content instead.
func (d *DocSync) OpenWith(ctx context.Context, absPath string, content []byte) (int32, error)

// Change replaces the whole document (opening it first if needed).
func (d *DocSync) Change(ctx context.Context, absPath string, content []byte) (int32, error)

// Save sends didSave (with text when the server asked for it).
func (d *DocSync) Save(ctx context.Context, absPath string) error

// Close sends didClose and forgets the document.
func (d *DocSync) Close(ctx context.Context, absPath string) error

func (d *DocSync) IsOpen(absPath string) bool
func (d *DocSync) Content(absPath string) (content []byte, version int32, ok bool)
func (d *DocSync) Count() int

// Reopen re-sends didOpen for every document from disk; used as the
// instance's OnRestart. Documents that vanished are forgotten.
func (d *DocSync) Reopen(ctx context.Context)
```

规则：

1. 文本检查：`len > MaxDocumentBytes` → `ErrFileTooLarge`；含 `0x00` 或 `!utf8.Valid` → `ErrNotText`。检查在 `EnsureSynced`、`OpenWith`、`Change` 都做。
2. `EnsureSynced`：`os.Stat`；已打开且 `mtime`/`size` 都没变 → 直接返回缓存；否则读盘算 sha256，与缓存相同只更新 mtime/size；不同则 `didChange`（`{"textDocument": {"uri", "version": v+1}, "contentChanges": [{"text": 全文}]}`）；未打开则 `didOpen`（`version = 1`，`languageId = languageFor(path)`）。
3. 打开新文档前，若已达 `maxOpen`，对 `lastUsed` 最早的文档发 `didClose`。
4. `Save`：`inst.Capabilities().SaveIncludesText()` 为真时带 `text`。
5. 同一个 `DocSync` 的所有通知在持有 `d.mu` 时发送，保证单文档内顺序。
6. 所有路径先 `filepath.Clean`；URI 用 `PathToURI`。

### 步骤 2：`diagnostics.go` —— 存储

```go
// Problem is one diagnostic as the runtime keeps it.
type Problem struct {
	ServerID  string
	Path      string // absolute
	Severity  int    // 1..4; 0 from the server is stored as 1
	Line      int    // LSP 0-based
	Character int    // LSP 0-based, in the server's encoding
	Source    string
	Code      string
	Message   string
}

// DiagStore keeps the latest problems per (server, file) and lets callers wait for them.
type DiagStore struct { /* unexported */ }

func NewDiagStore() *DiagStore

// Publish records a publishDiagnostics (or a pull result) and wakes waiters.
func (s *DiagStore) Publish(serverID, absPath string, version *int32, diags []Diagnostic)

// Get returns the latest problems for one file.
func (s *DiagStore) Get(serverID, absPath string) (problems []Problem, version *int32, seq uint64, ok bool)

// Seq is the number of publishes so far; callers remember it to ask "since when".
func (s *DiagStore) Seq() uint64

// WaitFor blocks until (serverID, absPath) is published after afterSeq with a
// version >= minVersion (any version when the publish carried none), or ctx ends.
func (s *DiagStore) WaitFor(ctx context.Context, serverID, absPath string, minVersion int32, afterSeq uint64) bool

// WaitQuiet blocks until no publish for serverID has arrived for quiet, or until deadline/ctx.
func (s *DiagStore) WaitQuiet(ctx context.Context, serverID string, quiet time.Duration, deadline time.Time)

// PublishedSince lists files of serverID published after afterSeq.
func (s *DiagStore) PublishedSince(serverID string, afterSeq uint64) []string

// Subscribe calls fn after every publish (outside the store lock); cancel stops it.
func (s *DiagStore) Subscribe(fn func(serverID, absPath string)) (cancel func())

// Counts sums errors and warnings across a server's files.
func (s *DiagStore) Counts(serverID string) (errors, warnings int)

// Forget drops a file's problems (document closed or deleted).
func (s *DiagStore) Forget(serverID, absPath string)

// Snapshot copies every file's current problems for serverID; DidWrite
// takes one before an edit as the baseline for files it did not write.
func (s *DiagStore) Snapshot(serverID string) map[string][]Problem
```

实现：互斥锁 + 条件通知（每次 Publish 关闭并替换一个 `chan struct{}`，等待者 select 它与 `ctx.Done()`，被唤醒后重查条件）。

### 步骤 3：`diagnostics.go` —— pull、增量、格式化

```go
// PullDiagnostics asks the server for one document's diagnostics
// (textDocument/diagnostic) and publishes the full report and any related
// documents into the store. It reports false when the server lacks the capability.
func PullDiagnostics(ctx context.Context, inst *Instance, store *DiagStore, serverID, absPath string) (bool, error)

// ProblemKey is the identity used by NewProblems (spec §8.3.4).
func ProblemKey(p Problem) string

// NewProblems returns the problems in after that are not in before, as a multiset.
func NewProblems(before, after []Problem) []Problem

// SeverityThreshold maps "error"/"warning"/"information"/"hint" to 1..4 ("" means warning).
func SeverityThreshold(name string) int

// ReportOptions shapes model-facing diagnostic text.
type ReportOptions struct {
	ProjectRoot string
	MinSeverity int      // from SeverityThreshold
	MaxPerFile  int
	MaxFiles    int
	FirstPaths  []string // files this edit wrote, listed first in this order
	// Position maps a problem to a 1-based line and column (the caller uses
	// FromLSPPosition with the file content and the server's encoding).
	Position func(p Problem) (line, column int)
}

// FormatEditDiagnostics renders appendix B.3; text is "" when there is nothing to say.
func FormatEditDiagnostics(problems []Problem, pending []string, servers []string, baselineUnavailable bool, opts ReportOptions) (text string, summary event.LSPDiagnosticsSummary)

// LateChange is one file's late news: New problems, or Cleared when none remain.
type LateChange struct {
	Path    string
	New     []Problem
	Cleared bool
}

// FormatLateDiagnostics renders appendix B.4; "" when changes is empty after filtering.
func FormatLateDiagnostics(changes []LateChange, opts ReportOptions) string
```

规则：

1. `PullDiagnostics`：`Capabilities().Supports("diagnosticProvider")` 为假 → `(false, nil)`；否则 `Call("textDocument/diagnostic", {"textDocument": {"uri"}})`，`kind == "full"` 时 `Publish(items)`；`unchanged` 时不改存储；`relatedDocuments` 里的每个 full 报告也 Publish（`URIToPath`）。
2. `ProblemKey`：`fmt.Sprintf("%d\x1f%s\x1f%s\x1f%s", severity, source, code, normalized message)`，message 去首尾空白并把连续空白压成一个空格。
3. `NewProblems`：统计 before 中每个键的数量；遍历 after **从后往前**，某键剩余计数 > 0 则计数减一并跳过，否则加入结果；最后把结果反转回原顺序（保证「取编辑后列表中该键靠后的条目」）。
4. 过滤与排序（`FormatEditDiagnostics` 与 `FormatLateDiagnostics` 共用）：丢弃 `Severity > MinSeverity`；文件顺序 = `FirstPaths` 中出现的按其顺序，其余按相对路径字典序；文件内按 severity、行、列；每文件最多 `MaxPerFile`，最多 `MaxFiles` 个文件；项目根之外的文件丢弃。
5. 行文本逐字照附录 B.3：`  {severity} {line}:{column} {message} [{source} {code}]`；severity 用 `SeverityName`；方括号内为空的部分省略、都空则省略方括号；message 中的 `\r\n`、`\n` 替换为 ` / `；路径用 `filepath.Rel(ProjectRoot, p)` 再 `filepath.ToSlash`。
6. `FormatEditDiagnostics` 的首行、截断行、pending 行、「只有 pending」的形态逐字照附录 B.3；`summary` 填 `New`（过滤后的总数）、`Files`、`Servers`、`Items`（按输出顺序，最多 `MaxFiles*MaxPerFile` 条）、`PendingFiles`（相对路径）、`BaselineUnavailable`。没有问题也没有 pending → 返回 `("", 空 summary)`。
7. `FormatLateDiagnostics` 逐字照附录 B.4；`Cleared` 的文件写一行 `{relative path}: no remaining problems`。

### 步骤 4：更新 `doc.go`，写测试

`doc.go` 文件清单加：`docsync.go (keeping each server's open documents in step with the disk), diagnostics.go (the diagnostics store, new-problem computation and the model-facing diagnostic text)`。

**验证**：`CGO_ENABLED=1 go test -tags fts5 ./pkg/lsp -count=1 -race -timeout 5m` → `ok`。

### 步骤 5：全量与包图

**验证**：`gofmt -l pkg/lsp`、`go vet ./pkg/lsp` 干净；`scripts/package-graph.sh && git status --short pkg/architecture/testdata/graph.json` → ` M`（提交）；`CGO_ENABLED=1 go test -tags fts5 ./pkg/architecture -count=1` → `ok`。

## 测试计划

- `docsync_test.go`（用任务 05 的 `startFake`）
  - `TestEnsureSyncedOpensThenChanges`：第一次 → record 里 `didOpen` version 1；未改文件再调用 → 没有新消息；改文件 → `didChange` version 2 且 `contentChanges[0].text` 为新内容。
  - `TestOpenWithBaselineThenChange`：`OpenWith(旧内容)` → `didOpen` 文本为旧内容；`Change(新内容)` → `didChange`。
  - `TestSaveIncludesTextOnlyWhenAsked`：两个子测试，capabilities 分别带与不带 `textDocumentSync.save.includeText`。
  - `TestLRUCloseOldest`：`maxOpen = 2`，打开 3 个 → 第一个收到 `didClose`。
  - `TestRejectsLargeAndBinary`：4 MiB+1 的文件 → `ErrFileTooLarge`；含 NUL → `ErrNotText`；record 中没有它们的 `didOpen`。
  - `TestReopenAfterRestart`：把 `docs.Reopen` 作为 `OnRestart`，`crash_after_requests` 触发重启后 record 里再次出现同一 URI 的 `didOpen`。
- `diagnostics_test.go`
  - `TestStorePublishGetAndWait`：`WaitFor` 在 Publish 之前阻塞、之后返回 true；`minVersion` 大于发布版本时继续等待至 ctx 超时返回 false；`Seq` 递增；`PublishedSince` 只列之后发布的文件。
  - `TestSnapshotIsACopy`：`Snapshot` 之后再 Publish，快照内容不变。
  - `TestWaitQuiet`：在 50ms、100ms 各 Publish 一次，`quiet = 80ms`，返回时间 ≥ 180ms；`deadline` 更早时按 deadline 返回。
  - `TestPullDiagnosticsWithFake`：假服务器 `pull_diagnostics` 打开 → 返回 true、存储里有条目；capabilities 无 `diagnosticProvider` → 返回 false。
  - `TestNewProblemsIgnoresLineShift`：before 有 A@10，after 有 A@12 与 B@3 → 只有 B；before 两个 A、after 三个 A → 一个 A（靠后的那个）；code 数字与字符串都参与键。
  - `TestFormatEditDiagnosticsGolden`：固定输入与附录 B.3 逐字比对，覆盖：1 条（`problem` 单数）、多文件含编辑文件在前、source/code 缺省组合、多行 message、`max_per_file` 截断行、`max_files` 截断行、基线不可用首行、只有 pending、问题 + pending。
  - `TestFormatLateDiagnosticsGolden`：新问题与 `Cleared` 各一个文件，与附录 B.4 逐字比对。
  - `TestSeverityThreshold`：四个名字与空串。

## 完成判据

- [ ] `CGO_ENABLED=1 go test -tags fts5 ./pkg/lsp -count=1 -race -timeout 5m` 通过
- [ ] `ls pkg/lsp/*.go | grep -v _test.go | wc -l` 比任务开始时多 2（应为 15，若任务 06 已合入）
- [ ] golden 测试逐字覆盖附录 B.3、B.4 的每种形态
- [ ] `gofmt`、`go vet` 干净；`graph.json` 已提交
- [ ] README 中任务 07 状态为 `DONE`

## STOP 条件

- 附录 B.3/B.4 的某种形态无法用本任务的函数表达（需要改规范）。
- 需要修改 `instance.go` 超出 `consumePullStale` 一个方法。

## 维护说明

- `FormatEditDiagnostics` / `FormatLateDiagnostics` 的输出进入模型上下文：改动它们等于改模型可见文本，必须同步改规范附录 B 与 golden 测试。
- `NewProblems` 不用行号做键是有意的（规范 §8.3.4），评审时不要「修正」成按位置比较。
