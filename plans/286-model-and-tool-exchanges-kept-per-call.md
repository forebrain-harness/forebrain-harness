# Plan 286: 模型调用与工具调用按「一次调用一份」留证，不同会话永不混写

> **执行者须知**：逐步执行，每步跑完验证命令、对照预期再往下。遇到「STOP 条件」任一条，停下报告，不要自行发挥。
> **禁止任何 git commit / push**（owner 手动审阅提交），改动只留在工作区。完成后更新 `plans/README.md` 里本计划那一行的状态；若派发你的评审方说明由它维护索引，则跳过。
>
> **Drift check（开工前只跑一次）**：
>
> ```sh
> SCOPE="pkg/telemetry/http_log.go pkg/telemetry/http_log_test.go pkg/telemetry/tool_debug.go pkg/telemetry/debuglog.go pkg/telemetry/debuglog_test.go pkg/telemetry/logfile.go pkg/telemetry/logfile_test.go pkg/tool/registry.go pkg/process/open.go cmd/cachebaseline/main.go"
> git status --porcelain -- $SCOPE
> git diff 17b4633a -- $SCOPE
> ```
>
> 预期：只出现 285 对 `pkg/telemetry/debuglog.go`（`LogProviderDebug` 函数体）与 `pkg/process/open.go`（`RegisterScope`）的改动。其它输出逐条对照下文「现状」的**代码内容**；内容对不上即 STOP（行号漂移不算）。

## 状态

- **优先级**：P1（owner 点名的问题：并发会话的 SSE 响应混在一起无法定位）
- **工作量**：M
- **风险**：MED（改的是每一次模型请求都经过的 `http.RoundTripper`；写错会改变或卡住流式响应）
- **依赖**：`plans/284-structured-event-log-foundation.md`、`plans/285-all-log-writers-use-the-event-log.md`（都必须已 DONE）
- **类别**：bug / dx（可诊断性）
- **制定基线**：`17b4633a`，分支 `dev`，2026-11-11

## Owner 裁决（2026-11-11）

- 请求体：**失败、断流、未完成的调用全留；成功的只留头尾各 32KB**。响应一律全留。
- 报文与记录分开存，都在文件里；保留 7 天、总量 1GB（284 已实现清理）。

## 为什么要做

这是 owner 提出本轮工作的直接原因，也是 `fix-gemini-bare-invalid-argument` 那份计划查不下去的原因。2026-11-11 在 owner 机器的 `~/.forebrain/logs` 上实测：

1. **看不出一段输出属于谁**。`llm_http request` / `llm_http response` 块只有 `id=<pid>-<seq>`，没有会话、轮次、代理；`tool_call` / `tool_result` 块连 id 都没有（`pkg/telemetry/tool_debug.go:21-27`）。`debug.log.1` 里 4 个进程、多个会话与并行子代理的块首尾相接，排查时只能读请求体里的系统提示词猜归属。
2. **失败请求的证据被截掉**。`formatHTTPRequestBlock`（`pkg/telemetry/http_log.go:285-308`）对请求体做 `BoundText(…, DefaultRecordBytes)`：1.1MB 的请求只留头尾各 64KB。2026-11-08 那次 Gemini `[400] Request contains an invalid argument.` 丢的正是中间 877KB，事后无法定因。
3. **证据只活两小时**。`debug.log` 20MB × 4 份；实测 `debug.log.1` 只覆盖 09:36–11:35。那次事故的请求在排查开始前已被轮转覆盖。按内容统计：请求块占 55%（每个约 130KB，已截断）、响应块占 40%。**原始 SSE 用 gzip 可压到 1/30**（8.36MB → 0.25MB），请求体约 1/3。
4. **流没结束就什么都没有**。流式响应先整段攒在内存里（`observationBuffer`，`limit <= 0`），流结束才一次写盘（`http_log.go:164-169`）。进程在流中途挂死或被杀——最需要证据的场景——磁盘上只有请求块。每个在途流还多占一份完整拷贝的内存。
5. **块格式解析不了**。块以**尾**标记 `==== llm_http ====` 结束、没有起始标记；工具结果里只要出现同样的文字（代理读 `debug.log` 自查时必然出现）就把块切错。实测 8 个块的开头被前一条无标记的行吞进去，90 个响应只配上 88 个请求。
6. **请求头脱敏靠黑名单**。`redactHeaderValue`（`http_log.go:357-364`）只认 7 个头名；自定义接入点带的任何其它凭据头会原样落盘。同文件的 `redactURL` 对查询参数用的是关键词规则（名字含 `token`/`key`/`secret`/`password`/`auth`/`signature`），两套规则不一致。

做完之后：一次调用 = 两条带完整身份的记录 + 它自己的两个压缩文件；失败调用的请求体完整可重放；流式响应边到边落盘；网页（287/288）按会话筛出来的每一行点开就是它自己的请求与响应。

本计划同时**取代** `fix-gemini-bare-invalid-argument` 计划的 §3.2（4xx/5xx 请求体分块写 `request_part`）：这里失败请求体整份保存为一个文件，不需要分块。该计划的 §3.1（无细节 400 的自愈）与本计划无关，不受影响。

## 现状（执行者需要的事实，全部内联）

### 284/285 提供的接口（已存在，直接用）

```go
telemetry.DefaultEventLog() *EventLog
(*EventLog).Proc() string                                         // 本进程的 6 位随机数
(*EventLog).BlobPath(t time.Time, name string) (string, error)    // <home>/logs/<日期>/x/<name>
telemetry.Emit(ctx, level, kind, msg string, attrs ...slog.Attr)  // 身份取自 ctx（285 已注册 Scope）
telemetry.BlobRef{Name, Bytes, Whole, Omitted}
```

`Emit` 目前只接受 `slog.Attr`。本计划需要把 `BlobRef` 放进记录的 `Blobs`：约定属性键以 `blob.` 开头、值是 `slog.AnyValue(BlobRef{…})` 时进 `Blobs[去掉前缀的键]`（在 `pkg/telemetry/eventlog.go` 的属性归类处加这一条，配一条测试）。

### 要替换的代码

`pkg/telemetry/http_log.go`：

```go
func WrapLLMHTTPTransport(next http.RoundTripper) http.RoundTripper {
	if next == nil {
		next = http.DefaultTransport
	}
	transport := newHTTPTraceRoundTripper(next)
	if debugFileLogEnabled() {
		transport = newHTTPDebugFileLogRoundTripper(transport)
	}
	return transport
}
```

`httpDebugFileLogRoundTripper.RoundTrip`（同文件 `:142-183`）读完整个请求体、写请求块、调用下游、对 `text/event-stream` 响应套一层 `newObservingReadCloser(resp.Body, 0, finalize)`，其余响应 `io.ReadAll` 后写响应块。`httpTraceRoundTripper`（`:46-130`，OTel span）**与本计划无关，保持原样**，它继续用 `newObservingReadCloser(resp.Body, DefaultRecordBytes, …)`。

`pkg/telemetry/tool_debug.go` 的 `NewToolDebugFileLogMiddleware()`：调用前写 `tool_call` 块（`arguments_raw` 全量——owner 在 plan 274 的决定：模型写出的工具参数必须全量），调用后写 `tool_result` 块（结果 `BoundText(…, DefaultRecordBytes)`）。它在 `pkg/tool/registry.go:269` 被装在中间件链的最外层。`formatToolResultValue` / `serializeContentParts` 同时被 `pkg/telemetry/tool_trace.go` 使用，**保留**。

开关：`debugFileLogEnabled()`（`pkg/telemetry/debuglog.go:19-25`）读环境变量 `FOREBRAIN_LLM_HTTP_DEBUG`，值为 `0`/`false`/`no`/`off` 时关闭，默认开。

### 可复用

- `BoundText`、`RedactLogLine`、`redactURL`、`formatRedactedHeaderLines`、`isEventStreamResponse`、`snapshotHTTPResponseMetadata`（都在 `pkg/telemetry`）。
- 既有测试脚手架：`pkg/telemetry/http_log_test.go` 的 `TestHTTPDebugFileLogKeepsWholeEventStream`（`:472`）、`TestHTTPDebugFileLogPairsRequestAndResponse`（`:615`）——`t.Setenv("FOREBRAIN_HOME", tmp)`、`FOREBRAIN_LLM_HTTP_DEBUG=1`、`httptest.NewServer` 回一段 SSE。新测试照它们的结构写。

## 需要的命令

| 用途 | 命令 | 成功时 |
|------|------|--------|
| 编译 | `CGO_ENABLED=1 go build -tags fts5 ./...` | 退出码 0 |
| 本包测试（含竞态） | `CGO_ENABLED=1 go test -tags fts5 -race ./pkg/telemetry -count=1` | `ok` |
| 上游回归 | `CGO_ENABLED=1 go test -tags fts5 ./pkg/llm/... ./pkg/tool ./pkg/run ./pkg/process -count=1` | 全 `ok` |
| 架构门 | `CGO_ENABLED=1 go test -tags fts5 ./pkg/architecture -count=1` | `ok` |
| vet / 格式 | `go vet ./... && gofmt -l pkg cmd` | 无输出 |

## 范围

**In scope**：
- `pkg/telemetry/exchange.go`、`pkg/telemetry/exchange_test.go`（新建）
- `pkg/telemetry/http_log.go`、`http_log_test.go`（删掉文件日志那一半，保留 OTel 那一半与脱敏函数）
- `pkg/telemetry/tool_debug.go`（中间件函数体）、新建 `pkg/telemetry/tool_debug_test.go`（若不存在）
- `pkg/telemetry/eventlog.go`、`eventlog_test.go`（只加 `blob.` 属性归类）
- `pkg/telemetry/debuglog.go`、`debuglog_test.go`、`pkg/telemetry/logfile.go`、`logfile_test.go`（删除不再使用的部分，见 Step 5）
- `pkg/tool/registry.go`（仅当中间件构造函数改名时）

**Out of scope**：
- `httpTraceRoundTripper`、`pkg/telemetry/otel.go`、`span_helpers.go`、`tool_trace.go`——OTel 链路不动，span 属性上限不变。
- `pkg/llm/**` 的任何适配器——不改请求装配、不改流解析。
- `pkg/run/attempt_ledger.go` 与 `fb_llm_attempts` 表——那是轮次级账本，继续存在；本计划不往状态库写任何东西。
- `fix-gemini-bare-invalid-argument` 计划 §3.1 的 `geminiRefusalKeywords`——另一件事。

## 设计（定稿，照此实现）

### 调用 ID 与文件

- 调用 ID：`<proc>-<6 位序号>`，如 `a3f09c-000391`；`proc` 来自 `EventLog.Proc()`，序号是进程内 `atomic.Uint64`。全局唯一（proc 每次启动随机），不含 pid，不怕 pid 复用。
- 报文文件（gzip，`0o600`），与记录同一个日期目录：
  - `x/<id>.req.gz`：`POST <脱敏 URL> HTTP/1.1\n` + 脱敏后的请求头行 + 空行 + 请求体（过 `RedactLogLine`）。
  - `x/<id>.resp.gz`：`HTTP/2.0 200 OK\n` + 脱敏后的响应头行 + 空行 + 响应体原文（过 `RedactLogLine`）。
- 工具调用 ID：`<proc>-t<6 位序号>`；大的参数与结果用 `x/<id>.args.gz`、`x/<id>.result.gz`。

### 请求头脱敏：与 `redactURL` 同一条规则

`redactHeaderValue(name, value)` 改为：头名（小写）**包含** `auth`、`key`、`token`、`secret`、`password`、`cookie`、`credential`、`signature` 任一子串 → `[REDACTED]`；否则 `RedactLogLine(value)`。原来的 7 个名字都被这条规则覆盖（`authorization`、`proxy-authorization`、`x-api-key`、`api-key`、`x-auth-token`、`cookie`、`set-cookie`）。`x-opencode-session` 不含这些子串，照常显示。

### 一次调用的时间线

```
t0  读完请求体 ──► 写 x/<id>.req.gz（整份）──► Emit DEBUG kind=llm msg="llm request"
                                                 attrs: x, method, url, model, req_bytes, stream
                                                 blob.req = {Name, Bytes, Whole:true}
    调用下游 RoundTrip
      ├─ 返回 error ──► Emit WARN "llm response" outcome=transport_error（或 cancelled，见下）, err, dur_ms
      └─ 拿到响应头
           ├─ 非流式：读完响应体 ──► 写 x/<id>.resp.gz ──► 收尾
           └─ 流式：把 resp.Body 换成 teeBody —— 每次 Read 到的字节同步写进 gzip.Writer，
              每累计 32KB 或距上次 Flush 超过 1 秒就 Flush 一次（磁盘上随时有到目前为止的内容）；
              EOF / 出错 / Close 时收尾（sync.Once）
收尾 ──► 关闭 resp 文件
     ──► 若 outcome=ok：把 req 文件原子替换成「头 32KB + 省略标记 + 尾 32KB」
     ──► Emit "llm response"
         attrs: x, method, url, model, status, outcome, dur_ms, ttfb_ms, max_gap_ms, req_bytes, resp_bytes, stream, [read_err]
         blob.req = {…, Whole:<是否整份>, Omitted:<省略字节>}
         blob.resp = {…, Whole:true}
```

- `outcome` 取值与级别：

| outcome | 条件 | 级别 |
|---------|------|------|
| `ok` | 状态码 2xx 且响应体读到 EOF 无错 | INFO |
| `http_error` | 状态码不是 2xx | WARN |
| `read_error` | 响应体读取中途出错（断流） | WARN |
| `transport_error` | 下游 `RoundTrip` 返回错误 | WARN |
| `cancelled` | 上面两种错误且 `errors.Is(err, context.Canceled)`，或响应体在 EOF 之前被 `Close` | INFO |

  级别是 WARN 而不是 ERROR：重试、脱签名重发、续写这些自愈层在它之上，一次调用失败不等于一轮失败；一轮失败由 285 的 `turn failed`（ERROR）表达。

- **只有 `ok` 才把请求体降为头尾**。其余四种（含进程在收尾前死掉、根本没有第二条记录的情况）请求文件保持整份——这就是 owner 说的「失败、断流、未完成的全留」。
- 降为头尾的写法：新内容 = `BoundText(整份文本, 64*1024)`，写到 `x/<id>.req.gz.tmp` 后 `os.Rename` 覆盖（原子；读取方要么读到整份要么读到头尾）。`BlobRef.Bytes` 始终是**原始**未压缩字节数，`Omitted` 是省略的字节数。请求体不超过 64KB 时不动，`Whole` 保持 `true`。
- `ttfb_ms`：发出请求到下游 `RoundTrip` 返回（拿到响应头）。`max_gap_ms`：从拿到响应头起，相邻两次读到数据之间的最大间隔（含头到第一块、最后一块到 EOF）。这两个数让「卡了 58 秒后断流」在记录里一眼可见，不用读原文。
- `model`：先取请求 URL 路径里 `/models/<名字>` 之后到 `:` 或 `/` 之前的一段（Gemini）；否则在请求体前 4KB 里按 `"model"\s*:\s*"([^"]+)"` 取第一处；都没有则空。这是尽力而为的展示字段，注释里写明。
- `url` 用 `redactURL`。
- `debugFileLogEnabled()` 为假时：两条记录照写（元数据仍然有用），但**不写任何报文文件**、记录不带 `blob.*`。函数改名 `payloadCaptureEnabled()`，环境变量名不变。
- 写报文失败（磁盘满等）：不影响请求本身；记录照写、对应的 `blob.*` 省略、加属性 `blob_error=<错误>`。**任何留证代码路径都不得让 `RoundTrip` 返回它自己的错误**。
- 响应体透传必须保持原样：`teeBody.Read` 先调下游 `Read`，把读到的 `p[:n]` 写进 gzip，再原样返回 `(n, err)`；不缓冲、不改变分块边界、不吞错误。

### 工具调用

`NewToolDebugFileLogMiddleware` 改名 `NewToolEventMiddleware`（`pkg/tool/registry.go:269` 同步改），行为：

```
调用前  Emit DEBUG kind=tool msg="tool call"    attrs: tool, tool_call=<id>, args_bytes, args=<内联> 或 blob.args
调用后  Emit INFO|WARN kind=tool msg="tool result" attrs: tool, tool_call=<id>, dur_ms, result_bytes, [err], result=<内联> 或 blob.result
```

- 参数是模型的输出，**全量**：≤ 8KB 内联在 `args` 属性里（284 的单属性上限正是 8KB），更大则整份写 `x/<id>.args.gz`，记录里带 `args_preview`（前 512 字节）与 `blob.args`。
- 结果不是模型输出：先 `BoundText(formatToolResultValue(out), DefaultRecordBytes)`（128KB，与今天一致），≤ 8KB 内联，否则写 `x/<id>.result.gz`，带 `result_preview` 与 `blob.result`（`Whole`/`Omitted` 如实）。
- 处理函数返回错误 → 级别 WARN、`err` 属性；否则 INFO。
- `DefaultRecordBytes` 常量从 `logfile.go` 挪到 `exchange.go`（`tool_trace.go` 与 `http_log.go` 的 OTel 部分仍在用）。

## 步骤

### Step 1：`blob.` 属性归类

`pkg/telemetry/eventlog.go` 的属性归类处加一条：键以 `blob.` 开头且值是 `BlobRef` → `rec.Blobs[strings.TrimPrefix(key, "blob.")]`。`eventlog_test.go` 加 `TestEmitRoutesBlobAttrs`。

**验证**：`CGO_ENABLED=1 go test -tags fts5 ./pkg/telemetry -run 'TestEmitRoutesBlobAttrs' -count=1` → `ok`。

### Step 2：`exchange.go`——调用留证的 RoundTripper

新建 `pkg/telemetry/exchange.go`：`exchangeRoundTripper`、`teeBody`、`writeGzip(path string, parts ...[]byte)`、`boundRequestBlob(path)`、`requestModel(req, body)`、`DefaultRecordBytes`。`WrapLLMHTTPTransport` 改为：

```go
	transport := newHTTPTraceRoundTripper(next)
	return newExchangeRoundTripper(transport)   // 始终安装；是否写报文由 payloadCaptureEnabled() 在每次调用时决定
```

（原来「开关关闭就不安装」意味着关掉后连元数据都没有；现在元数据总有。）

**验证**：`CGO_ENABLED=1 go build -tags fts5 ./...` → 退出码 0。

### Step 3：测试钉住行为

新建 `pkg/telemetry/exchange_test.go`，见「测试计划」。先写 `TestExchangeConcurrentStreamsStayInTheirOwnFiles` 与 `TestExchangeKeepsWholeRequestWhenRefused`，确认它们在 Step 2 的实现上通过。

**验证**：`CGO_ENABLED=1 go test -tags fts5 -race ./pkg/telemetry -run 'TestExchange' -count=1` → `ok`。

### Step 4：工具中间件

改写 `pkg/telemetry/tool_debug.go` 的中间件函数体并改名；`pkg/tool/registry.go:269` 同步。`formatToolResultValue`、`serializeContentParts` 不动。

**验证**：`CGO_ENABLED=1 go test -tags fts5 ./pkg/telemetry ./pkg/tool -count=1` → `ok`。

### Step 5：删除旧路径

- `pkg/telemetry/http_log.go`：删除 `httpDebugFileLogRoundTripper`、`newHTTPDebugFileLogRoundTripper`、`httpDebugSeq`、`formatHTTPRequestBlock`、`formatHTTPResponseBlock`、`formatHeaders`。保留 `httpTraceRoundTripper`、`observingReadCloser`/`observationBuffer`（OTel 仍用带上限的那种）、`isEventStreamResponse`、`snapshotHTTPResponseMetadata`、`formatRedactedHeaderLines`、`redactHTTPBody`、`redactHeaderValue`、`redactURL`。
- `pkg/telemetry/debuglog.go`：删除 `withDebugFileLog`、`writeDebugFileLog`、`writeDebugFileLogWhole`、`debugFileLogFile` 等包级状态；保留（改名后的）`payloadCaptureEnabled` 与 285 已改好的 `LogProviderDebug`。
- `pkg/telemetry/logfile.go`：确认 `File`、`Open`、`OpenRaw`、`RotateOnce`、`RawWriter`、`WriteRaw`、`RotateActive`、`rotateActiveLocked`、`StartPeriodicRotation`、`SyncRaw`、`CloseRaw`、`Options` 全仓无引用后删除（`grep -rn "telemetry\.\(Open\|OpenRaw\|WriteRaw\|RawWriter\|RotateOnce\|RotateActive\|StartPeriodicRotation\|SyncRaw\|CloseRaw\|Options\)\b" pkg cmd --include='*.go'`）；`BoundText` 留下（可并入 `eventlog.go` 后删掉整个 `logfile.go` 与 `logfile_test.go`，把 `BoundText` 的既有测试挪到 `eventlog_test.go`）。
- 相应测试：`http_log_test.go` 里 `TestHTTPDebugFileLog*` 五条由 `exchange_test.go` 的对应用例取代后删除；`TestObservingReadCloserUnboundedKeepsWholeStream` 若 `limit <= 0` 分支不再有调用方，连同该分支一起删。

**验证**：
```sh
grep -rn "debug\.log\|==== \|writeDebugFileLog\|httpDebugFileLog" pkg cmd --include='*.go' | grep -v _test.go | grep -v "pkg/telemetry/eventlog.go"
```
→ 无输出。`CGO_ENABLED=1 go test -tags fts5 -race ./pkg/telemetry -count=1` → `ok`。

### Step 6：全量回归

**验证**：「需要的命令」表逐条全绿。

### Step 7：真机验收（fake provider，隔离 home）

按 `.claude/skills/run-forebrain` 的配方：同一个隔离 home 下**同时**起两个 TUI（两个会话），各发一条会触发工具调用的消息；另让 fake provider 对其中一次请求回 400（`.claude/skills/run-forebrain/fake_provider.py` 支持的方式；该文件当前有另一会话的未提交改动，只读不改）。核对：

```sh
H=<隔离 home>; D="$H"/logs/*/
ls "$H/logs"                                                   # 只有日期目录；根下没有 debug.log
jq -r 'select(.kind=="llm" and .msg=="llm response")|[.session,.attrs.x,.attrs.status,.attrs.outcome,.blobs.req.whole]|@tsv' $D/tui-*.jsonl
#   每行 session 非空；两个 TUI 的 session 不同；400 那行 outcome=http_error、whole=true；200 且请求体 > 64KB 的行 whole=false
for f in $D/x/*.resp.gz; do gzip -t "$f" || echo BAD "$f"; done  # 无 BAD
X=<上面 400 那行的 x>; gzip -dc $D/x/$X.req.gz | tail -c +1 | wc -c  # ≈ 该行 req_bytes + 头部长度（整份）
jq -r 'select(.kind=="tool")|[.session,.attrs.tool,.attrs.tool_call,.msg]|@tsv' $D/tui-*.jsonl | sort -k3 | head
#   同一个 tool_call 恰好两行（tool call / tool result），session 相同
```

逐条贴进执行报告。fake provider 做不到的项（例如真实 1MB 请求体）写进「owner 手动验收」清单。

## 测试计划

`pkg/telemetry/exchange_test.go`（脚手架照 `http_log_test.go` 的 `TestHTTPDebugFileLogKeepsWholeEventStream`；读记录用 285 留下的事件文件读取助手或就地 `bufio.Scanner` + `json.Unmarshal`）：

- `TestExchangeConcurrentStreamsStayInTheirOwnFiles`：**本计划的核心回归**。`httptest` 服务按请求头 `X-Marker` 回 200 条各带该标记的 SSE 事件，事件之间 `Flush` 并小睡 1ms；16 个 goroutine 并发请求、各用不同标记、各自的 `ctx` 带不同会话（测试里 `RegisterScope` 一个从 ctx 取值的函数）。断言：16 个 `.resp.gz` 每个只含自己的标记（`strings.Count` 其它 15 个标记均为 0）且恰好 200 条；16 条 `llm response` 记录的 `session` 与各自标记一一对应。`-race` 下通过。
- `TestExchangeKeepsWholeRequestWhenRefused`：300KB 请求体首尾各埋唯一标记、中间也埋一个，下游回 400。`.req.gz` 解压后三个标记都在、不含 `log bytes omitted`；记录 `outcome=http_error`、`lvl=WARN`、`blobs.req.whole=true`。
- `TestExchangeBoundsRequestAfterSuccess`：同样的请求体，下游回 200。解压后首尾标记在、中间标记不在、`log bytes omitted` 恰好 1 处；`blobs.req.whole=false`、`omitted` = 300KB − 64KB 对应的字节数、`bytes` 仍是原始长度。
- `TestExchangeSmallRequestStaysWhole`：10KB 请求体 + 200 → `whole=true`，文件未被替换。
- `TestExchangeStreamIsOnDiskBeforeItEnds`：下游先发 40KB 事件后挂住不结束；客户端读到这些字节后（流未关），直接 `gzip` 解压 `.resp.gz`（允许 `io.ErrUnexpectedEOF`）已能读出 ≥ 32KB；此时已有 `llm request` 记录、还没有 `llm response` 记录；`.req.gz` 是整份。
- `TestExchangeReportsCutStream`：下游发一半后掐断连接 → `outcome=read_error`、`read_err` 非空、`.req.gz` 整份、`.resp.gz` 含已收到的部分。
- `TestExchangeReportsTransportError`：下游地址不可达 → 一条 `llm response`，`outcome=transport_error`，无 `blob.resp`。
- `TestExchangeCancelIsNotAWarning`：读到一半 `cancel()` → `outcome=cancelled`、`lvl=INFO`、`.req.gz` 整份。
- `TestExchangePassesBodyThroughUnchanged`：客户端读到的字节与下游发出的逐字节相等（流式与非流式各一次），且流式场景下客户端能在流结束前读到第一块（照 `TestHTTPDebugFileLogRoundTripperPassesThroughEventStreamBeforeEOF` 的断言）。
- `TestExchangeMeasuresFirstByteAndLongestGap`：下游发头后睡 120ms 再发体、中间再睡 200ms → `ttfb_ms` < 100、`max_gap_ms` ≥ 180。
- `TestExchangeRedactsHeadersByNameRule`：请求头 `X-Custom-Api-Key`、`Authorization`、`X-Upstream-Token` 的值在 `.req.gz` 里是 `[REDACTED]`，`X-Opencode-Session` 原样。
- `TestExchangeModelFromBodyOrPath`：OpenAI 形状取请求体 `model`；`/v1beta/models/gemini-4-argon:streamGenerateContent` 取路径。
- `TestExchangeWithoutPayloadCaptureKeepsMetadata`：`FOREBRAIN_LLM_HTTP_DEBUG=0` → 两条记录都在、`x/` 目录不存在。
- `TestExchangeSurvivesUnwritablePayloadDir`：把 `x` 预先建成普通文件 → 请求照常成功、记录带 `blob_error`。

`pkg/telemetry/tool_debug_test.go`：
- `TestToolEventsPairByCallID`：一次调用得到两条记录，`tool_call` 相同，身份取自 ctx。
- `TestToolArgumentsAreKeptWhole`：200KB 参数 → `blob.args.whole=true`，解压后与原文逐字节相等。
- `TestToolResultIsBoundedOnce`：300KB 结果 → `blob.result.whole=false`，解压后 `log bytes omitted` 恰好 1 处。
- `TestToolHandlerErrorIsAWarning`。

## 完成判据（全部满足）

- [ ] `CGO_ENABLED=1 go build -tags fts5 ./...` 退出码 0；`go vet ./...`、`gofmt -l pkg cmd` 无输出
- [ ] `CGO_ENABLED=1 go test -tags fts5 -race ./pkg/telemetry -count=1` 为 `ok`，上面 18 个新测试都存在且通过
- [ ] 「需要的命令」表里的上游回归与架构门全 `ok`
- [ ] Step 5 的 grep 无输出
- [ ] `grep -n "func (.*httpTraceRoundTripper) RoundTrip" pkg/telemetry/http_log.go` 仍有 1 处，且 `git diff 17b4633a -- pkg/telemetry/http_log.go` 里该函数体没有改动行
- [ ] Step 7 的核对全部符合，输出贴进执行报告
- [ ] 改动文件 ⊆ 「范围」；`plans/README.md` 状态行已更新

## STOP 条件

- 284 或 285 未完成。
- `TestExchangePassesBodyThroughUnchanged` 无法通过（说明留证代码改变了响应流）——不要调整断言，回报。
- `pkg/llm/...` 的任何既有测试因本计划变红（说明适配器依赖了旧 RoundTripper 的某个副作用，例如请求体被整读并重置）——回报具体用例。注意：旧实现会 `io.ReadAll(req.Body)` 再把 `req.Body` 换成 `bytes.NewReader`，新实现必须保持这一点。
- 需要改 `httpTraceRoundTripper` 才能通过测试。
- `-race` 报出数据竞争且两次修复尝试后仍在。

## 非功能要求

| 维度 | 风险 | 对策 | 验证 |
|------|------|------|------|
| 延迟 | 请求路径上多一次整份 gzip（1MB 约 10–20ms） | 用 `gzip.BestSpeed`；相对一次模型请求（秒级）可忽略；不 `fsync` | 加 `BenchmarkExchangeRequestBlob`（1MB 请求体），结果写进执行报告，预期 < 30ms |
| 延迟 | 流式响应每次 `Read` 多一次压缩写 | 压缩写进 64KB 的 `bufio.Writer`，按「32KB 或 1 秒」Flush，不逐块落盘 | `TestExchangePassesBodyThroughUnchanged` 的「流未结束即可读到首块」 |
| 内存 | 旧实现每个在途流攒一份完整拷贝 | 改为边读边写盘，内存只有固定大小的缓冲 | 代码评审：`teeBody` 不持有随流增长的切片 |
| 磁盘 | 每天几千次调用 | 响应压缩约 1/30；成功请求只留 64KB（压缩后约 20KB）；失败整份；284 的 7 天 / 1GiB 封顶，先删报文 | 284 的 `TestSweepLogsDropsOldDaysThenOldestPayloads` |
| 鲁棒 | 留证失败影响请求 | 所有留证错误只进 `blob_error`，不向上返回 | `TestExchangeSurvivesUnwritablePayloadDir` |
| 鲁棒 | 进程在流中途死掉 | 请求文件发出前已整份落盘；响应文件定期 Flush | `TestExchangeStreamIsOnDiskBeforeItEnds` |
| 安全 | 凭据进日志（网页将能看到这些内容） | 头名规则与 `redactURL` 统一；URL 查询参数沿用 `redactURL`；请求体沿用 `RedactLogLine` | `TestExchangeRedactsHeadersByNameRule` |
| 可维护 | 新协议接入 | 留证层不解析协议（只取 `model` 用于展示）；解析在 287 的读取侧 | — |

## 维护注意

- **请求体降级只发生在 `ok`**。以后若有人想「省磁盘」把 4xx 也降级，就把这次事故的证据再丢一次。
- 请求体过 `RedactLogLine` 会把 `Bearer xxx` 字样替换掉；除此之外是逐字节原文，可用于重放。
- 报文里有完整对话内容与系统提示词，与状态库里的会话内容属于同一敏感级别；网页访问走网关登录（287）。
- 评审重点：`teeBody` 的 `Read`/`Close` 是否可能双重收尾（`sync.Once`）；原子替换是否用了同目录临时文件；`Emit` 用的是 `req.Context()`（身份来源）而不是 `context.Background()`。
- 已知未做（有意）：按 SSE 事件记录到达时间线（现在只有首字节与最长静默两个数）；按会话保留「最近 N 次完整请求」。
