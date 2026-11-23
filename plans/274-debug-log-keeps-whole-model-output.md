# Plan 274: debug.log 完整记录模型输出（不再截断，请求/响应可配对）

> **Executor instructions**: 按步骤顺序执行，每步验证通过再做下一步。遇到「STOP conditions」就停下报告，
> 不要自己发挥。**不要 commit / push / merge**：改动留在工作区（仓库主人手动提交）。
> 做完后把 `plans/README.md` 里本计划那一行的状态改成 DONE 并附一句摘要——除非派你来的评审说索引由他维护。
>
> **Drift check (run first)**:
> `git status --porcelain -- pkg/telemetry/` 与 `git diff --stat 0a698bf2 -- pkg/telemetry/`
> → 期望都为空（写计划时 `pkg/telemetry/` 无未提交改动；基线 `0a698bf2` 即当时的 HEAD）。
> （不要用 `git diff 0a698bf2..HEAD`：它比的是两个 commit，**看不到未提交改动**，基线即 HEAD 时恒为空。）
> 非空时：用 `git show 0a698bf2:<path>` 对照「Current state」的摘录，结构对得上就继续，对不上就 STOP。
> 本计划只动 `pkg/telemetry/`，与 270–273 无文件交集，执行顺序不受它们影响。

## Status

- **Priority**: P1（owner 2026-11-07：「debug.log 日志里的大模型输出内容必须完整打印，不要截断，严重影响排查和定位问题」）
- **Effort**: S–M
- **Risk**: LOW–MED（只改诊断日志；风险是内存与磁盘占用，以及误改 OTel span 的上限）
- **Depends on**: none
- **Category**: dx（可观测性）
- **Planned at**: commit `0a698bf2`, 2026-11-07

## Why this matters

`~/.forebrain/logs/debug.log` 是**唯一**保存 provider 原始响应的地方（状态库只存解析后的消息）。
plan 273 的取证（zhipuai/glm-5.3 跑了 20 分钟后整轮报 `empty assistant response after tool execution`）
正是卡在这里：事故那次调用的 SSE 响应在日志里只剩前 128 KB，结尾被换成 `[telemetry omitted 3253 bytes]`
——而 SSE 的**结尾**恰恰是 `finish_reason` 和 `usage` 所在的地方，于是无法判断 provider 当时为什么停；
同时并行会话的请求块与响应块交错写入，响应块里只有 `for_request: POST <url>`，分不清是哪次请求的回复。
做完后：模型输出（流式与非流式响应体、模型写出的工具参数）**原样完整**写入 debug.log；
每对请求/响应带同一个 `id=`，可以直接配对。

## Current state

所有路径相对仓库根。`pkg/telemetry/` 在写计划时无未提交改动。

- `pkg/telemetry/http_log.go:26-35` `WrapLLMHTTPTransport`：总是套 OTel 的 `httpTraceRoundTripper`，
  debug 日志开启时（`FOREBRAIN_LLM_HTTP_DEBUG` 未设或非 `0/false/no/off`，**默认开**，见
  `pkg/telemetry/debuglog.go:19-25`）再在外层套 `httpDebugFileLogRoundTripper`。
  两个 round tripper 都用同一个 `newObservingReadCloser` 观察 SSE 响应体（`:96`、`:155`）。

- **截断点①：SSE 观察缓冲只留前 128 KB**（`pkg/telemetry/http_log.go:195-222`）：

```go
// boundedObservationBuffer keeps HTTP telemetry observational: an arbitrarily
// long event stream must not become an arbitrarily large in-process buffer.
// The wire response is never altered; only the diagnostic snapshot is capped.
type boundedObservationBuffer struct {
	buf     bytes.Buffer
	omitted int64
}

func (b *boundedObservationBuffer) Write(p []byte) {
	remaining := DefaultRecordBytes - b.buf.Len()
	...
	b.omitted += int64(len(p))
}

func (b *boundedObservationBuffer) Bytes() []byte {
	result := append([]byte(nil), b.buf.Bytes()...)
	if b.omitted > 0 {
		result = append(result, []byte(fmt.Sprintf("\n[telemetry omitted %d bytes]\n", b.omitted))...)
	}
	return result
}

func newObservingReadCloser(body io.ReadCloser, finalize func([]byte, error)) io.ReadCloser {
	return &observingReadCloser{body: body, finalize: finalize}
}
```

  `DefaultRecordBytes = 128 * 1024`（`pkg/telemetry/logfile.go:22`）。

- **截断点②：响应块与请求块的 body 各被 `BoundText` 头尾截一次**（`pkg/telemetry/http_log.go:265-303`）：

```go
func formatHTTPRequestBlock(req *http.Request, body []byte) string {
	...
	fmt.Fprintf(&b, "llm_http request ts=%s\n", ts)
	...
	b.WriteString("body:\n")
	b.WriteString(BoundText(RedactLogLine(string(body)), DefaultRecordBytes))   // :280
	...
}

func formatHTTPResponseBlock(resp *http.Response, body []byte) string {
	...
	fmt.Fprintf(&b, "llm_http response ts=%s\n", ts)                           // :290
	if resp.Request != nil {
		fmt.Fprintf(&b, "for_request: %s %s\n", resp.Request.Method, redactURL(resp.Request.URL))
	}
	...
	b.WriteString("body:\n")
	b.WriteString(BoundText(RedactLogLine(string(body)), DefaultRecordBytes))   // :298
	...
}
```

  `BoundText`（`pkg/telemetry/logfile.go:345-356`）保留头尾各一半，中间换成 `\n... [N log bytes omitted] ...\n`。

- **截断点③：整块写盘时再截一次**（`pkg/telemetry/debuglog.go:27-47` → `pkg/telemetry/logfile.go:158-162`）：

```go
func writeDebugFileLog(section, msg string) {
	...
	if debugFileLogFile != nil {
		_ = debugFileLogFile.WriteSection(section, msg)
	}
}

// WriteSection writes one bounded diagnostic block using a consistent format.
func (f *File) WriteSection(section, text string) error {
	_, err := fmt.Fprintf(f, "%s\n==== %s ====\n", BoundText(text, DefaultRecordBytes), section)
	return err
}
```

  所以今天一个请求块（头部 + 已截到 128 KB 的 body）会被**再截一次**，出现两个省略标记。

- **截断点④：模型写出的工具参数**（`pkg/telemetry/tool_debug.go:24`）：
  `fmt.Fprintf(&b, "arguments_raw:\n%s\n", BoundText(arguments, DefaultRecordBytes))`；
  工具结果 `:34` `BoundText(formatToolResultValue(out), DefaultRecordBytes)`（工具结果不是模型输出，本计划保留其上限）。
  两者都经 `writeDebugFileLog`（`:25`、`:40`）→ `WriteSection` 再截一次。

- **保留不动的唯一上限**：`File.writeLocked`（`pkg/telemetry/logfile.go:126-134`）在单条记录超过整个文件上限
  `MaxBytes`（默认 `DefaultFileMaxBytes = 20 MB`，`:20`）时截断。理由：轮转 `rotateActiveLocked`
  （`:285-311`）只把文件**最后** `MaxBytes` 字节拷进 `.1`，超过 20 MB 的单条记录即使整条写进去，
  下次轮转也会丢掉它的开头；放开它不能真正保住全文，反而让轮转语义失效。现实中单次模型响应远小于 20 MB
  （32000 输出 token 的冗长 SSE 也只有几 MB）。

- OTel span 的 `forebrain.http.response_body` 属性（`pkg/telemetry/http_log.go:104`、`:124`）——
  span 属性会发往外部导出器，**保持 128 KB 上限不变**。

- `debug.log` 的请求块与响应块没有配对标识：响应块只有 `for_request: POST <url>`；
  同一个 `debug.log` 被同一进程的多个会话/子代理、乃至多个 forebrain 进程（多个 TUI、gateway）以
  `O_APPEND` 共同写入。进程内单块写入受 `debugFileLogMu` 保护、是一次 `write(2)`，块本身不会被拆开，
  但块的先后会交错。

- 现有测试：`pkg/telemetry/http_log_test.go:323` `TestHTTPDebugFileLogRoundTripperPassesThroughEventStreamBeforeEOF`
  （debug.log 路径的 SSE 夹具，含 `resetEmitForTest(t)`、`t.Setenv("FOREBRAIN_HOME", tmp)`、
  `t.Setenv("FOREBRAIN_LLM_HTTP_DEBUG", "1")`、`roundTripFunc` 的用法——新测试照它写）；
  `:402` `TestObservingReadCloserPreservesReadAndFinalizesOnce` 与 `:429`
  `TestObservingReadCloserBoundsTelemetrySnapshot`（断言 `telemetry omitted 65536 bytes`，**OTel 路径仍需要这个行为**）；
  `:385`/`:393` 用子串 `"llm_http response ts="` 判断响应块是否已写——**新增的 id 必须放在 `ts=` 之后，不能插在前面**。
  `resetEmitForTest` 定义在 `pkg/telemetry/debuglog_test.go:185`。
- 仓库内没有任何代码或脚本解析 `debug.log` 的 `llm_http` 块（已 grep 全仓，除 `pkg/telemetry` 自身与测试外零命中）。

## Commands you will need

| Purpose | Command | Expected |
|---|---|---|
| 构建 | `CGO_ENABLED=1 go build ./...` | exit 0 |
| 本包测试 | `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/telemetry/ -count=1` | `ok`（写计划时基线已实测为 ok） |
| vet | `go vet ./pkg/telemetry/` | 无输出 |
| 格式 | `gofmt -l pkg/telemetry` | 无输出 |

（`FOREBRAIN_HOME` 必须显式指向临时目录：不继承外层环境，仓库硬规矩。链接时 `libtokenizers.a ... newer 'macOS' version`
的 ld 警告是既有噪音，不是失败。）

## Scope

**In scope**（只改这些）：
- `pkg/telemetry/http_log.go`
- `pkg/telemetry/logfile.go`（只**新增** `WriteSectionWhole`，不改 `WriteSection`/`writeLocked`/轮转）
- `pkg/telemetry/debuglog.go`（只**新增** `writeDebugFileLogWhole`）
- `pkg/telemetry/tool_debug.go`
- `pkg/telemetry/http_log_test.go`、`pkg/telemetry/debuglog_test.go`、`pkg/telemetry/logfile_test.go`

**Out of scope**（不要碰）：
- `File.writeLocked` 的 20 MB 单条上限与 `rotateActiveLocked` 的轮转语义（理由见 Current state）。
- `DefaultFileMaxBytes` / `DefaultBackups`（debug.log 的保留量）——要调保留量另立计划。
- OTel span 属性的上限（`httpTraceRoundTripper` 继续传 128 KB）。
- 请求体的上限（见 Design decisions 2）。
- `LogProviderDebug`（`pkg/telemetry/debuglog.go:53`）与 `info.log` / `error.log` / gateway 日志。
- `pkg/llm/**`、`pkg/run/**`（有并行会话的未提交改动，且与本计划无关）。

## Design decisions

1. **模型输出一律原样完整**：LLM HTTP 响应体（SSE 与非 SSE、含非 200 的错误体）、模型写出的工具参数
   （`tool_call` 的 `arguments_raw`）写进 debug.log 时**不经任何** `BoundText`/观察缓冲上限。
2. **请求体保留一次 128 KB 头尾截断**：请求体是整段对话历史（每次调用重复一遍，动辄数百 KB），
   完整内容已在状态库 `fb_messages`；全量写入会让 debug.log 的 80 MB 保留量（20 MB × 4 个文件）只装得下几十次调用。
   但只截**一次**（今天被 `formatHTTPRequestBlock` 与 `WriteSection` 截两次）。
3. **工具结果保留一次 128 KB 上限**（不是模型输出），同样只截一次。
4. **OTel span 不变**：仍是 128 KB 上限（外部导出器有属性大小限制）。
5. **请求/响应配对**：每次 round trip 分配一个 `id=<pid>-<seq>`（`seq` 为进程内原子递增），
   请求块、响应块、`response_error`、`response_read_body_error` 都带同一个 id；放在 `ts=` 之后。
6. 已知代价（写进代码注释）：debug 日志默认开启，SSE 观察缓冲不再封顶，每个进行中的流会在内存里多留一份
   完整原始字节，直到流结束写盘——与客户端本身累积全文同一量级。

## Steps

### Step 1: 观察缓冲的上限改成参数（OTel 有上限，debug.log 无上限）

`pkg/telemetry/http_log.go`：

- `boundedObservationBuffer` 加字段 `limit int`；`Write` 在 `limit <= 0` 时全部写入、不累计 `omitted`；
  `limit > 0` 时行为与今天逐字节相同（只是把 `DefaultRecordBytes` 换成 `b.limit`）。更新类型注释：

```go
// observationBuffer snapshots a response body for diagnostics; the wire
// response is never altered. limit > 0 caps the snapshot (the OTel span: its
// attributes go to external exporters); limit <= 0 keeps every byte (debug.log:
// it is the only record of what the provider actually sent, and the end of an
// event stream — finish reason, usage — is exactly the part a cap drops).
```

  （类型名可保留 `boundedObservationBuffer`，也可改为 `observationBuffer`；改名就同步改所有引用。）
- `newObservingReadCloser(body io.ReadCloser, limit int, finalize func([]byte, error)) io.ReadCloser`：
  把 `limit` 传进 `buf`。
- 调用点：`:96`（`httpTraceRoundTripper`）传 `DefaultRecordBytes`；`:155`（`httpDebugFileLogRoundTripper`）传 `0`。
- 测试调用点同步改：`pkg/telemetry/http_log_test.go:411` 传 `DefaultRecordBytes`（该测试只验转发与只结束一次，与上限无关）、
  `:432` 传 `DefaultRecordBytes`（**断言不改**：OTel 路径仍需「封顶 + `telemetry omitted 65536 bytes`」）。

**Verify**: `CGO_ENABLED=1 go build ./...` → exit 0；
`FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/telemetry/ -count=1` → `ok`。

### Step 2: 写盘不再二次截断

- `pkg/telemetry/logfile.go`：在 `WriteSection` 之后**新增**（不要改 `WriteSection` 本身——`LogProviderDebug` 等其它调用方继续用它）：

```go
// WriteSectionWhole writes one diagnostic block as given. The caller has
// already bounded whatever part of text needed bounding — or, for a model's
// output, deliberately bounded nothing. writeLocked's whole-file guard still
// applies to a record larger than the file itself.
func (f *File) WriteSectionWhole(section, text string) error {
	_, err := fmt.Fprintf(f, "%s\n==== %s ====\n", text, section)
	return err
}
```

- `pkg/telemetry/debuglog.go`：新增 `writeDebugFileLogWhole(section, msg string)`，与 `writeDebugFileLog` 同形
  （同样的 `debugFileLogEnabled` 判断、`debugFileLogOnce` 打开、`debugFileLogMu` 加锁），只是调用 `WriteSectionWhole`。
  为避免两份打开逻辑分叉，可把共同部分抽成一个内部 helper（例如 `withDebugFileLog(func(*File))`），二者共用。
- `pkg/telemetry/http_log.go` 里 `httpDebugFileLogRoundTripper.RoundTrip` 的**全部** `writeDebugFileLog("llm_http", …)`
  （`:145`、`:148`、`:157`、`:159`、`:166`、`:172` 共 6 处）改为 `writeDebugFileLogWhole("llm_http", …)`。
- `pkg/telemetry/tool_debug.go` 的两处（`:25` `tool_call`、`:40` `tool_result`）改为 `writeDebugFileLogWhole`。

**Verify**: `CGO_ENABLED=1 go build ./...` → exit 0；`grep -n 'writeDebugFileLog(' pkg/telemetry/http_log.go pkg/telemetry/tool_debug.go` → 无输出。

### Step 3: 响应体与工具参数不再 `BoundText`

- `pkg/telemetry/http_log.go:298`（`formatHTTPResponseBlock`）：`b.WriteString(RedactLogLine(string(body)))`——去掉 `BoundText`。
- `pkg/telemetry/http_log.go:280`（`formatHTTPRequestBlock`）：**保留** `BoundText(…, DefaultRecordBytes)`
  （Design decision 2），在它上方加一行注释说明「请求体是整段历史、已在状态库，只截这一次；响应体不截」。
- `pkg/telemetry/tool_debug.go:24`：`fmt.Fprintf(&b, "arguments_raw:\n%s\n", arguments)`——去掉 `BoundText`（模型写出的参数）。
- `pkg/telemetry/tool_debug.go:34`：**保留** `BoundText(formatToolResultValue(out), DefaultRecordBytes)`（工具结果）。

**Verify**: `grep -c 'BoundText' pkg/telemetry/http_log.go` → `1`；`grep -c 'BoundText' pkg/telemetry/tool_debug.go` → `1`；
`FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/telemetry/ -count=1` → `ok`。

### Step 4: 请求/响应配对 id

`pkg/telemetry/http_log.go`：

- 包级 `var httpDebugSeq atomic.Uint64`（`sync/atomic`）；`RoundTrip` 开头
  `id := fmt.Sprintf("%d-%d", os.Getpid(), httpDebugSeq.Add(1))`。
- `formatHTTPRequestBlock(req, body, id)` / `formatHTTPResponseBlock(resp, body, id)` 加 `id string` 形参，首行改为
  `llm_http request ts=%s id=%s` / `llm_http response ts=%s id=%s`（**`ts=` 在前**：测试与人工 grep 都按
  `"llm_http response ts="` 前缀找块）。
- 三条单行错误也带上：`llm_http response_error id=%s method=%s url=%s err=%v`、
  `llm_http response_read_body_error id=%s status=%d err=%v`（两处）。

**Verify**: `CGO_ENABLED=1 go build ./...` → exit 0；`go vet ./pkg/telemetry/` → 无输出。

### Step 5: 测试

在 `pkg/telemetry/http_log_test.go` 新增（照 `:323` 的夹具：`t.TempDir()` + `logs` 目录、两条 `t.Setenv`、
`resetEmitForTest(t)`、`roundTripFunc`；读 `filepath.Join(tmp, "logs", "debug.log")`）：

| 测试名 | 场景 | 断言 |
|---|---|---|
| `TestObservingReadCloserUnboundedKeepsWholeStream` | `newObservingReadCloser(…, 0, …)`，payload = `DefaultRecordBytes + 64*1024` 字节 | finalize 拿到的字节 == payload；不含 `telemetry omitted` |
| `TestHTTPDebugFileLogKeepsWholeEventStream` | SSE 响应体 ≥ 300 KB（多行 `data: {...}\n\n`），最后一帧是唯一标记 `data: {"finish_reason":"stop","usage":{"completion_tokens":688}}` | debug.log 含该最后一帧原文；不含 `telemetry omitted`、不含 `log bytes omitted` |
| `TestHTTPDebugFileLogKeepsWholeUnaryResponse` | `Content-Type: application/json`、≥ 300 KB 的 JSON 响应体，末尾带唯一标记 | debug.log 含末尾标记；不含 `log bytes omitted` |
| `TestHTTPDebugFileLogBoundsRequestBodyOnce` | 请求体 ≥ 300 KB | debug.log 里 `log bytes omitted` **恰好出现 1 次**（`strings.Count`） |
| `TestHTTPDebugFileLogPairsRequestAndResponse` | 一次普通 round trip | 用正则 `llm_http request ts=\S+ id=(\S+)` 与 `llm_http response ts=\S+ id=(\S+)` 各取到 id，二者相等且非空；连发两次，两次的 id 不同 |
| `TestHTTPTraceSpanStillBoundsResponseBody` | 照 `:52` `TestHTTPTraceRoundTripperRedactsHTTPSpanPayload` 的 span recorder 夹具，SSE 响应体 = `DefaultRecordBytes + 64*1024` | span 的 `forebrain.http.response_body` 长度 ≤ `DefaultRecordBytes + 128` 且含 `telemetry omitted` |

`pkg/telemetry/debuglog_test.go` 新增 `TestToolDebugKeepsWholeArguments`：经 `NewToolDebugFileLogMiddleware()` 包一个
返回小结果的 handler，传入 ≥ 300 KB 的 arguments（末尾唯一标记）→ debug.log 含该标记、不含 `log bytes omitted`。

`pkg/telemetry/logfile_test.go` 新增 `TestWriteSectionWholeDoesNotBound`：对 `t.TempDir()` 下 `Open` 的文件写
`DefaultRecordBytes*2` 字节的文本 → 文件内容含全文、不含 `log bytes omitted`。

**Verify**: `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/telemetry/ -count=1 -v -run 'Unbounded|KeepsWhole|BoundsRequestBodyOnce|PairsRequestAndResponse|StillBoundsResponseBody|WriteSectionWhole'`
→ 8 个新测试全部 `--- PASS`（数一下：`grep -c -- '--- PASS'` 的输出 ≥ 8，且无 `--- FAIL`）。

## Done criteria

全部满足：

- [ ] `CGO_ENABLED=1 go build ./...` exit 0
- [ ] `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/telemetry/ -count=1` → `ok`
- [ ] `go vet ./pkg/telemetry/` 无输出；`gofmt -l pkg/telemetry` 无输出
- [ ] `grep -c 'BoundText' pkg/telemetry/http_log.go` → `1`（只剩请求体）；`grep -c 'BoundText' pkg/telemetry/tool_debug.go` → `1`（只剩工具结果）
- [ ] `grep -n 'writeDebugFileLog(' pkg/telemetry/http_log.go pkg/telemetry/tool_debug.go` → 无输出
- [ ] `grep -n 'newObservingReadCloser(resp.Body, DefaultRecordBytes' pkg/telemetry/http_log.go` → 恰 1 处（OTel 路径）；
      `grep -n 'newObservingReadCloser(resp.Body, 0' pkg/telemetry/http_log.go` → 恰 1 处（debug.log 路径）
- [ ] `TestObservingReadCloserBoundsTelemetrySnapshot` 的断言未改、仍通过
- [ ] `git status --porcelain -- pkg/telemetry/` 只列出 Scope 里的文件；`git status` 里 `pkg/telemetry/` 以外的条目与开工前一致

## STOP conditions

- `pkg/telemetry/` 在开工时已有未提交改动，且与「Current state」的摘录对不上。
- 发现仓库里有代码或脚本按 `llm_http response ts=` 之后的**整行**格式解析 debug.log（加 `id=` 会破坏它）——
  写计划时已 grep 为零命中；若出现新命中，先报告。
- 改完后 `TestObservingReadCloserBoundsTelemetrySnapshot` 或任何 OTel span 测试变红
  （说明你把上限从 OTel 路径也拿掉了）。
- 需要改 `writeLocked`、`rotateActiveLocked` 或 `DefaultFileMaxBytes` 才能让测试通过。
- 同一处验证连续失败两次。

## Maintenance notes

- **保留量**：debug.log 仍是 20 MB × 4 个文件（`DefaultFileMaxBytes`/`DefaultBackups`）。响应全量写入后，
  一个文件装的调用数会变少；若排查时发现要找的那次已被轮转掉，正确做法是另立计划调大 debug.log 的
  `MaxBytes`/`Backups`（`pkg/telemetry/debuglog.go` 的 `Open(p, Options{...})`），**不要**把截断加回来。
- **唯一剩下的上限**是单条记录超过整个文件上限（20 MB）时 `writeLocked` 的保护；轮转只保留文件最后
  `MaxBytes` 字节，所以放开它保不住全文。若真出现 >20 MB 的单次响应，同样走「调大 MaxBytes」。
- **跨进程轮转竞态（已知遗留）**：`rotateMu` 是进程内锁；两个 forebrain 进程同时写 debug.log 时，
  一个进程「拷贝尾部 → truncate」之间另一个进程追加的记录可能丢失。这是丢整条记录、不是截断，本计划不处理。
- **内存**：debug 日志默认开启，每个进行中的 SSE 流在内存里多一份完整原始字节直到流结束。若将来要给
  debug.log 路径加上限，必须同时保住**流的结尾**（finish reason / usage），即「保头保尾」而不是「只保头」。
- 评审要点：①OTel 路径的上限没被动到；②debug.log 里响应块不再出现任何省略标记；③请求块只有一个省略标记；
  ④`id=` 在 `ts=` 之后、请求与响应一致。
