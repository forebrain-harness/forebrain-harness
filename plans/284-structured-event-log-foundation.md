# Plan 284: 日志底座——每条记录自带身份的结构化事件日志（按天分目录、每进程一个文件）

> **执行者须知**：逐步执行，每步跑完验证命令、对照预期再往下。遇到「STOP 条件」任一条，停下报告，不要自行发挥。
> **禁止任何 git commit / push**（owner 手动审阅提交），改动只留在工作区。完成后更新 `plans/README.md` 里本计划那一行的状态；若派发你的评审方说明由它维护索引，则跳过。
>
> **Drift check（开工前只跑一次）**：本计划只**新增**文件，唯一会改的已有文件是 `pkg/telemetry/doc.go`。
>
> ```sh
> git status --porcelain -- pkg/telemetry
> git diff 17b4633a -- pkg/telemetry
> ls pkg/telemetry/eventlog.go pkg/telemetry/eventlog_test.go 2>/dev/null
> ```
>
> 预期：三条都无输出。前两条有输出时，逐条对照下文「现状」的**代码内容**；内容对不上即 STOP（行号漂移不算）。第三条有输出说明已有人开工，STOP。

## 状态

- **优先级**：P1（285–288 全部依赖它）
- **工作量**：M
- **风险**：LOW（纯新增；本计划结束时没有任何调用方，行为零变化）
- **依赖**：无
- **类别**：tech-debt / dx（可观测性）
- **制定基线**：`17b4633a`，分支 `dev`，2026-11-11。制定时工作区有另一会话的未提交改动（`pkg/tui/*.go`、`pkg/tool/session_tools*.go`、`pkg/process/planreview_images*.go`），与本计划的 `pkg/telemetry` 无交集。

## Owner 裁决（2026-11-11，四条都已拍板）

1. 日志**只写文件**，不进状态库：`logs/<日期>/<来源>-<主机>-<进程号>-<随机>.jsonl`，一行一条结构化记录；大报文单独存压缩文件。
2. 模型调用的请求体：失败、断流、未完成的**全留**；成功的只留头尾各 32KB。响应一律全留。**保留 7 天、总量 1GB 封顶**，超出先删最旧的报文。（实现在 286，本计划只提供容量清理。）
3. 网页日志页放「设置 · 高级 · 日志」（288）。
4. 旧的 `debug.log` / `info.log` / `error.log` / `gateway-<主机>.log` **直接替换**，不双写；旧文件到期由清理任务删除（切换在 285/286）。

## 为什么要做

今天的日志无法回答「这条是谁写的」。2026-11-11 在 owner 机器上实测（`~/.forebrain/logs`）：

- `debug.log.1`（20MB，只覆盖 09:36–11:35 两小时）里 **4 个进程**（pid 57999 / 59338 / 89572 / 93818）交错追加，`llm_http` 块、`tool_call` 块里**没有任何会话、轮次、项目、代理标识**；要知道一段 SSE 属于哪个会话只能读请求体猜。
- 同一个文件里混着三种格式：`==== llm_http ====` 尾标记块、`time=… level=…` 的 slog 文本行、`2026/11/11 09:48:21.726656 forebrain chat turn ok …` 的标准库行；尾标记块没有起始标记，8 个 `llm_http` 块的开头被前一条无标记的行吞进去，90 个响应只配得上 88 个请求。
- 轮转是「复制后截断」，且每个 TUI 进程各自每 2 秒检查同一批文件、进程间没有锁：两个进程同时判定超限会把备份多挪一档（丢 20MB），复制与截断之间别的进程写入的记录直接丢失。
- `error.log` 10.7MB / 52,716 行里 47,444 行是 WARN（见 285）；网关只写 `error.log` 不轮转（`appendErrorFile` 每条 `os.OpenFile`，轮转只有 TUI 的定时器在做）。
- 时间戳有 6 种写法，其中网关文件镜像只有 `18:18:11.905`、**没有日期**，无法按时间范围查。

这份计划建底座：一种记录格式、每条记录自带来源/进程/项目/会话/轮次/代理、每个进程写自己的文件（不再有跨进程交错与轮转竞争）、按天分目录（清理 = 删目录）。本计划结束时底座还没有调用方；285 把所有写入点迁过来，286 把模型调用与工具调用迁过来，287 提供查询，288 是网页。

## 现状（执行者需要的事实，全部内联）

- `pkg/telemetry/`：20 个生产文件（`pkg/architecture` 的 `TestPackagesStayUnderThirtyProductionFiles` 上限是 34；`TestFileNamesHaveAtMostTwoUnderscores` 限制文件名最多两个下划线；`TestTestFilesCorrespondToProductionFiles` 要求 `X_test.go` 旁边必须有 `X.go`）。
- `pkg/telemetry` 已有的导入边：`pkg/home`、`pkg/llm`、OTel。**不得新增其它 `pkg/*` 导入**（`pkg/architecture` 的 `TestSameLayerEdgesAreEnumerated` / `TestLayerCeiling` 会红）；特别是 `pkg/tool` 导入了 `pkg/telemetry`（`pkg/tool/registry.go:269`），反向导入会成环。
- 既有可复用的东西（**复用，不要另写一份**）：
  - `BoundText(text string, maxBytes int) string`（`pkg/telemetry/logfile.go:354-365`）：保头尾、中间写 `\n... [N log bytes omitted] ...\n`。
  - `RedactLogLine(s string) string`（`pkg/telemetry/redact.go`）：把 `Bearer xxx` 换成 `Bearer [REDACTED]`。
  - `home.LogsDir = "logs"`（`pkg/home/data.go:11`）、`home.Root()`。
  - 主机名规整逻辑今天在 `cmd/forebrain/gateway_log.go:35-51` 的 `replicaLogName()`（只留 `[A-Za-z0-9._-]`，其余换 `-`，首尾去 `-.`，空则 `local`）。本计划在 `pkg/telemetry` 里实现同一规则的 `HostLabel()`；285 再让 `cmd/forebrain` 改用它并删掉原函数。
- 上下文里已有的身份（本计划**不直接读**，通过端口注入，见 Step 3）：
  - 会话：`llm.AgentSessionIDFromContext(ctx)`（`pkg/llm/request_context.go:44`），子代理时它是子代理自己的转录会话。
  - 用户可见会话：`tool.ConversationSessionIDFromContext(ctx)`（`pkg/tool/state.go:291`，没设时回落到上一条）。
  - 轮次：`tool.RunIDFromContext(ctx)`（`pkg/tool/state.go:272`）。
  - 项目：`tool.ProjectKeyFromContext(ctx)`（`pkg/tool/state.go:430`）。
- 仓库惯例：注释写「为什么」，英文；错误不 panic；测试用 `t.TempDir()` + `t.Setenv("FOREBRAIN_HOME", tmp)`，参照 `pkg/telemetry/logfile_test.go`。

## 需要的命令

| 用途 | 命令 | 成功时 |
|------|------|--------|
| 编译 | `CGO_ENABLED=1 go build -tags fts5 ./...` | 退出码 0 |
| 本包测试 | `CGO_ENABLED=1 go test -tags fts5 ./pkg/telemetry -count=1` | `ok` |
| 竞态 | `CGO_ENABLED=1 go test -tags fts5 -race ./pkg/telemetry -run 'TestEventLog' -count=1` | `ok` |
| 架构门 | `CGO_ENABLED=1 go test -tags fts5 ./pkg/architecture -count=1` | `ok` |
| vet / 格式 | `go vet ./pkg/telemetry/ && gofmt -l pkg/telemetry` | 无输出 |

## 范围

**In scope**（只允许动这些）：
- `pkg/telemetry/eventlog.go`（新建）
- `pkg/telemetry/eventlog_test.go`（新建）
- `pkg/telemetry/doc.go`（包说明补一段）

**Out of scope**（看着相关也不要碰）：
- `pkg/telemetry` 的其它文件、任何调用方——迁移是 285/286 的事；本计划结束时旧日志路径必须原样工作。
- `pkg/state`、任何 SQL——owner 已裁决日志不进状态库。
- `forebrain.yaml` / 设置页——保留天数与容量是**固定常量**，不做配置（owner：面向 C 端，能固定就固定）。

## 设计（定稿，照此实现）

### 目录与文件

```
<home>/logs/
  2026-11-11/                                  ← 写入方本地日期
    tui-mbp-dev-89572-a3f09c.jsonl             ← <来源>-<主机>-<进程号>-<6 位十六进制>.jsonl
    tui-mbp-dev-89572-a3f09c.1.jsonl           ← 单文件满 32MB 后的续段（只增不改名）
    gateway-gw-0-4117-51be02.jsonl
    x/                                         ← 报文与大字段（286 写入）
      a3f09c-000391.req.gz
      a3f09c-000391.resp.gz
```

- 一个文件**只有一个写入者**：文件名里的 6 位随机数每次进程启动重新生成，所以即使两个容器同主机名同 pid 共享一个 home 也不会撞名。没有跨进程追加，就没有交错；没有改名与截断，就没有轮转竞争。
- 跨天：写入时发现本地日期变了，关掉旧文件、在新日期目录开新文件（同名）。
- 续段：当前文件 ≥ `eventFileMaxBytes`（32MB）时开 `…-<随机>.<n>.jsonl`，n 从 1 递增。

### 记录格式（一行一条 JSON，字段顺序固定）

```go
// Record is one line of the event log. Field order is the on-disk key order:
// time, level, kind and message come first so a plain `tail -f` still reads.
type Record struct {
	Time    time.Time         `json:"-"`                 // 写成 "t"，RFC3339、毫秒、带时区
	Level   string            `json:"lvl"`               // DEBUG | INFO | WARN | ERROR
	Kind    string            `json:"kind"`              // log | turn | llm | tool | http | setup | panic
	Msg     string            `json:"msg"`
	Project string            `json:"project,omitempty"` // 项目键
	Session string            `json:"session,omitempty"` // 用户可见的会话
	Worker  string            `json:"worker,omitempty"`  // 子代理自己的转录会话（与 Session 不同才写）
	Run     string            `json:"run,omitempty"`
	Agent   string            `json:"agent,omitempty"`
	At      string            `json:"at,omitempty"`      // 仓库相对路径:行号，如 pkg/run/runner.go:1356
	Err     string            `json:"err,omitempty"`
	Attrs   map[string]any    `json:"attrs,omitempty"`
	Blobs   map[string]BlobRef `json:"blobs,omitempty"`
}

// BlobRef points at one payload file under the same day directory's x/.
type BlobRef struct {
	Name    string `json:"name"`              // 文件名，如 a3f09c-000391.req.gz
	Bytes   int64  `json:"bytes"`             // 未压缩字节数
	Whole   bool   `json:"whole"`             // false = 只留了头尾
	Omitted int64  `json:"omitted,omitempty"` // 省略的字节数
}
```

- 落盘时 `"t"` 是第一个键：实现 `MarshalJSON` 或先手写 `{"t":"…",` 再拼接 `json.Marshal` 去掉首个 `{` 的结果，两种都行，测试钉住「行以 `{"t":"` 开头」。
- 来源、主机、进程号、随机数**不进记录**，在文件名里（287 的读取方从文件名还原）；每条记录省 60 字节。
- 单行上限 `recordMaxBytes = 64 * 1024`：`Msg` 超 8KB、`Err` 超 16KB、`Attrs` 里任何字符串值超 8KB（键名为 `stack` 的放宽到 32KB，给 panic 的调用栈用），各自用 `BoundText` 截（保头尾、带省略字节数）。整行仍超限时把 `Attrs` 替换为 `{"truncated": true, "bytes": <原长度>}`。**绝不按字节硬切一行**（切坏 JSON 就查不出来了）。
- `Msg`、`Err` 和每个字符串属性值写盘前过一遍 `RedactLogLine`。

### 包级入口

```go
// OpenEventLog opens this process's event log under <root>/logs. source is
// "tui", "gateway" or "cli". The file itself is created on the first Write, in
// the directory of that record's local date.
func OpenEventLog(root, source string) *EventLog

func (l *EventLog) Write(rec Record)                                   // 一条记录一次 write(2)；失败只计数
func (l *EventLog) BlobPath(t time.Time, name string) (string, error)   // <root>/logs/<日期>/x/<name>，并确保目录存在
func (l *EventLog) Proc() string                                        // 6 位随机数，286 用它起调用 ID
func (l *EventLog) Close() error

func SetDefaultEventLog(l *EventLog)   // 进程只设一次；nil 表示不记
func DefaultEventLog() *EventLog

// Emit writes one record through the default log. It is the entry for callers
// that are not slog (turn lifecycle, model exchanges, tool calls, setup).
func Emit(ctx context.Context, level slog.Level, kind, msg string, attrs ...slog.Attr)

// NewEventHandler adapts slog to the event log: every slog record becomes a
// kind="log" record.
func NewEventHandler(l *EventLog, level slog.Leveler) slog.Handler

// Scope is who a record belongs to; RegisterScope installs the one function
// that reads it off a context. pkg/telemetry cannot import pkg/tool (tool
// imports telemetry), so the composition root supplies it — the same port
// shape as llm.SetHTTPTransportWrapper.
type Scope struct{ Project, Session, Worker, Run, Agent string }
func RegisterScope(fn func(ctx context.Context) Scope)

func HostLabel() string                          // 规整后的主机名
func SweepLogs(root string, now time.Time)        // 保留策略，见下
```

实现要点：

- `Write`：持 `l.mu`；按 `rec.Time`（零值取 `time.Now()`）算本地日期，必要时换目录/续段；`os.OpenFile(path, O_APPEND|O_CREATE|O_WRONLY, 0o600)`；整行（含 `\n`）**一次** `Write`；不 `fsync`（理由与 `pkg/telemetry/logfile.go:108-115` 的现有注释相同：诊断日志不需要掉电持久，`write(2)` 已能挺过进程崩溃）。
- 写失败：`l.dropped.Add(1)`，不返回错误、不 panic、不往 stderr 打（TUI 的 stderr 被重定向）。下一条写成功的记录在 `Attrs` 里带 `"dropped_before": N` 并清零。
- `Emit` / `NewEventHandler` 取身份的顺序：先调注册的 `Scope` 函数；再让**显式属性覆盖**——属性键是 `project` / `session` / `worker` / `run` / `agent` 时，值进对应字段且**不再留在 `Attrs` 里**（今天有 22 处 `slog.Xxx(..., "session", sid, ...)`、另有写成 `session_id` / `run_id` 的，两种写法都认：`session_id`→Session、`run_id`→Run）。
- `NewEventHandler` 另认一个保留属性键 `kind`：值进记录的 `Kind`（不留在 `Attrs`），没有则 `Kind="log"`。285 的访问日志靠它标成 `http`。
- 属性键是 `err` 或 `error` 时进 `Err`：值是 `error` 取 `Error()`，并把 `errors.Unwrap` 链上每层的 `%T` 收进 `Attrs["err_chain"]`（最多 8 层）；今天 `formatErrorDetails` 把同一条消息以 `%v`、`%#v`、`Error()` 打三遍（`pkg/telemetry/error_write.go:78-92`），新格式只留一遍。
- `At`：`NewEventHandler` 从 `slog.Record.Source()` 取；把绝对路径或模块路径裁成从 `pkg/` 或 `cmd/` 开始（找不到就取 `filepath.Base`）。`Emit` 用 `runtime.Caller(1)` 同样处理。
- `NewEventHandler` 的 `WithAttrs` 把预绑定属性并入每条记录；`WithGroup` 把后续属性键加 `组名.` 前缀（今天 `tuiLogSink.WithGroup` 直接丢弃分组，`pkg/tui/render.go` 的 `WithGroup(string) slog.Handler { return s }`——不要学它）。

### 保留策略（固定常量，无配置）

```go
const (
	LogRetentionDays = 7
	LogBudgetBytes   = int64(1) << 30 // 1 GiB
	eventFileMaxBytes = int64(32) << 20
)
```

`SweepLogs(root, now)`，幂等、可被多个进程同时调用（所有删除忽略 `ENOENT`）：

1. `<root>/logs` 下名字匹配 `^\d{4}-\d{2}-\d{2}$` 的目录，日期早于 `now` 减 7 天的整目录删除。其它名字的目录一律不碰。
2. 统计剩余目录总字节数；超过 `LogBudgetBytes` 时，从**最旧的一天**开始删它的 `x/` 目录（报文），仍超再删下一天的 `x/`；所有 `x/` 都删完仍超，再从最旧的一天删整目录。**永不删除 `now` 当天的 `.jsonl`**。
3. `<root>/logs` 根下的旧文件——名字匹配 `debug.log*`、`info.log*`、`error.log*`、`gateway.log*`、`gateway-*.log*`——修改时间早于 7 天的删除（旧格式的收尾；285/286 之后它们不再被写）。

`OpenEventLog` 返回前同步跑一次 `SweepLogs`，之后起一个每小时跑一次的 goroutine，`Close` 时停掉并等待退出。

## 步骤

### Step 1：记录、编码与写入器

新建 `pkg/telemetry/eventlog.go`：`Record`、`BlobRef`、`EventLog`、`OpenEventLog`、`Write`、`BlobPath`、`Proc`、`Close`、`HostLabel`。文件头注释说明三条不变量：一个文件一个写入者、一条记录一次写、只增不改名。

**验证**：`CGO_ENABLED=1 go build -tags fts5 ./pkg/telemetry/` → 退出码 0。

### Step 2：保留策略

同文件加 `SweepLogs` 与每小时的清理 goroutine。

**验证**：`go vet ./pkg/telemetry/` → 无输出。

### Step 3：身份端口、`Emit`、slog 适配

同文件加 `Scope`、`RegisterScope`、`SetDefaultEventLog`、`DefaultEventLog`、`Emit`、`NewEventHandler`。`RegisterScope` 未调用时 `Scope` 为空值（记录只有显式属性给出的身份）。

**验证**：`CGO_ENABLED=1 go build -tags fts5 ./...` → 退出码 0（确认没有引入环）。

### Step 4：测试

新建 `pkg/telemetry/eventlog_test.go`，见「测试计划」。

**验证**：`CGO_ENABLED=1 go test -tags fts5 -race ./pkg/telemetry -count=1` → `ok`。

### Step 5：包说明

`pkg/telemetry/doc.go` 补一段：事件日志是唯一的落盘日志格式、目录布局、三条不变量、保留常量。

**验证**：`CGO_ENABLED=1 go test -tags fts5 ./pkg/architecture -count=1` → `ok`。

## 测试计划

全部写在 `pkg/telemetry/eventlog_test.go`，结构照 `pkg/telemetry/logfile_test.go`（`t.TempDir()`，不碰真实 home）：

- `TestEventLogWritesOneJSONLinePerRecord`：写 3 条；文件路径匹配 `logs/<今天>/tui-<主机>-<pid>-[0-9a-f]{6}\.jsonl`；每行 `json.Valid`、以 `{"t":"` 开头、键顺序 `t,lvl,kind,msg`。
- `TestEventLogConcurrentWritersNeverInterleave`：32 个 goroutine 各写 500 条带自己编号的记录（`-race`）；读回 16,000 行，每行都是合法 JSON 且每个编号恰好 500 条。
- `TestEventLogTwoProcessesWriteTwoFiles`：同一 root 开两个 `EventLog`（模拟两个进程）；得到两个不同文件，各自只含自己的记录。
- `TestEventLogRollsToNextDayDirectory`：两条记录的 `Time` 跨本地午夜；落在两个日期目录。
- `TestEventLogStartsNewPartPastFileLimit`：把上限经包内变量调到 4KB，写到超限；出现 `.1.jsonl`，原文件未被改名、未被截断（大小只增）。
- `TestEventLogBoundsOversizedFieldsAndKeepsValidJSON`：`Msg` 100KB、属性值 100KB；该行 ≤ 64KB、`json.Valid`、含 `log bytes omitted`。
- `TestEventLogRedactsBearer`：`Msg` 含 `Bearer abc123`；落盘是 `Bearer [REDACTED]`。
- `TestEmitTakesScopeFromContextAndExplicitAttrsWin`：`RegisterScope` 返回 `Session:"s-ctx"`；一次不带属性、一次带 `slog.String("session","s-attr")`；分别得到 `s-ctx` 与 `s-attr`，且 `attrs` 里没有 `session` 键。`session_id` / `run_id` 写法同样生效。
- `TestEventHandlerMapsSlogRecord`：`slog.New(NewEventHandler(...)).Warn("m", "err", fmt.Errorf("wrap: %w", io.EOF), "n", 3)`；`lvl=WARN`、`kind=log`、`err="wrap: EOF"`、`attrs.err_chain` 两层、`attrs.n=3`、`at` 以 `pkg/telemetry/eventlog_test.go:` 开头。
- `TestEventHandlerKeepsGroups`：`WithGroup("http").Info("x","status",200)` → `attrs["http.status"]=200`。
- `TestEventHandlerPromotesKindAttr`：`Info("request","kind","http","status",200)` → `kind=http`，`attrs` 里没有 `kind`。
- `TestEventLogAllowsLongerStackAttr`：属性 `stack` 20KB 原样保留；同样长度的其它键被截到 8KB。
- `TestEventLogWriteFailureIsCountedNotReturned`：把日志目录替换成一个普通文件使打开失败；`Write` 不 panic；恢复后下一条带 `dropped_before`。
- `TestSweepLogsDropsOldDaysThenOldestPayloads`：造 10 个日期目录（其中 3 个早于 7 天）、把预算经包内变量调到很小；早于 7 天的全删；超预算时最旧一天的 `x/` 先没、当天的 `.jsonl` 仍在；名字不是日期的目录原样保留。
- `TestSweepLogsRemovesStaleLegacyFiles`：根下放 `debug.log.2`（mtime 8 天前）与 `error.log`（mtime 现在）；前者删、后者留。

## 完成判据（全部满足）

- [ ] `CGO_ENABLED=1 go build -tags fts5 ./...` 退出码 0
- [ ] `CGO_ENABLED=1 go test -tags fts5 -race ./pkg/telemetry -count=1` 为 `ok`，上面 15 个测试都存在且通过
- [ ] `CGO_ENABLED=1 go test -tags fts5 ./pkg/architecture -count=1` 为 `ok`
- [ ] `go vet ./pkg/telemetry/` 与 `gofmt -l pkg/telemetry` 无输出
- [ ] `git status --porcelain` 的新增/改动 ⊆ {`pkg/telemetry/eventlog.go`、`pkg/telemetry/eventlog_test.go`、`pkg/telemetry/doc.go`}（另一会话原有的未提交文件不算）
- [ ] `grep -rn "OpenEventLog\|telemetry.Emit(" pkg cmd --include='*.go' | grep -v "pkg/telemetry/"` 无输出（本计划不接调用方）
- [ ] `plans/README.md` 状态行已更新

## STOP 条件

- `pkg/telemetry` 需要导入 `pkg/home`、`pkg/llm` 之外的 `pkg/*` 才能编译。
- `pkg/architecture` 的任何测试变红且原因是本计划新增的文件。
- 需要改 `pkg/telemetry` 里除 `doc.go` 外的已有文件才能通过测试。
- 发现已有 `pkg/telemetry/eventlog.go`。

## 非功能要求

| 维度 | 风险 | 对策 | 验证 |
|------|------|------|------|
| 延迟 | 每条记录一次系统调用，落在模型请求与 HTTP 响应路径上 | 不 `fsync`、不逐条 `stat`（大小用内存计数，自己是唯一写入者所以准确）、文件句柄常开 | `go test -bench BenchmarkEventLogWrite ./pkg/telemetry`（加一个基准，记录写入 < 20µs/条写进执行报告） |
| 资源 | 报文把磁盘写满 | 7 天 + 1GiB 封顶，先删报文再删记录 | `TestSweepLogsDropsOldDaysThenOldestPayloads` |
| 鲁棒 | 日志目录不可写时拖垮业务 | `Write` 永不返回错误、永不 panic，只计数 | `TestEventLogWriteFailureIsCountedNotReturned` |
| 鲁棒 | 多副本共享 NFS 上的 home（`deploy/k8s/manifests/pvc.yaml` 要求 RWX） | 每个进程独占自己的文件，不依赖 NFS 上的 `O_APPEND` 原子性；清理只做幂等删除 | `TestEventLogTwoProcessesWriteTwoFiles` |
| 可维护 | 又长出第二套日志 | 285/286 结束时旧写入点全部删除；`doc.go` 写明这是唯一格式 | 285 的完成判据 grep |
| 可扩展 | 新增记录类型 | `Kind` 是开放字符串，读取方（287）按未知类型原样返回 | — |

## 维护注意

- **一个文件一个写入者**是整个设计的地基：任何「让两个进程写同一个文件」的改动都会把交错问题带回来。
- 保留天数与容量是常量；若以后要可配，按 owner 规则只进设置页并入库，不进 `forebrain.yaml`。
- 记录字段是落盘格式，287 的查询与 288 的页面都读它；加字段可以，改名与删字段要同时改三处。
- 评审重点：`Write` 是否真的一次 `write(2)`；超长字段是否走 `BoundText` 而不是切片；`SweepLogs` 是否可能删到非日期目录。
