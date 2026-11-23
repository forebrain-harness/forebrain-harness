# Plan 285: 所有写入点改走事件日志，修正级别与重复，删除旧文件写入

> **执行者须知**：逐步执行，每步跑完验证命令、对照预期再往下。遇到「STOP 条件」任一条，停下报告，不要自行发挥。
> **禁止任何 git commit / push**（owner 手动审阅提交），改动只留在工作区。完成后更新 `plans/README.md` 里本计划那一行的状态；若派发你的评审方说明由它维护索引，则跳过。
>
> **Drift check（开工前只跑一次）**：
>
> ```sh
> SCOPE="pkg/telemetry cmd/forebrain/root.go cmd/forebrain/gateway_log.go cmd/forebrain/console_log.go cmd/forebrain/serve.go cmd/forebrain/interactive.go pkg/tui/render.go pkg/tui/chat_session.go pkg/tui/chat_turn.go pkg/tui/chat_surface.go pkg/tui/chat_slash.go pkg/tui/notify.go pkg/tui/stderr_unix.go pkg/tui/platform_windows.go pkg/run/orchestration_llm.go pkg/process/open.go pkg/process/runtime.go pkg/turn/mcp_format.go pkg/gateway/slash_handlers.go pkg/agent/trace_context.go deploy/k8s/manifests/pvc.yaml"
> git status --porcelain -- $SCOPE
> git diff 17b4633a -- $SCOPE
> ```
>
> 制定本计划时，另一会话在 `pkg/tui/render.go`、`pkg/tui/chat_slash.go`、`pkg/tui/notify.go`（以及范围外的 `pkg/tui/run.go`、`pkg/tui/input_events.go`）有未提交改动。**这些文件出现在输出里是预期的**：先问清那一会话是否已收尾；未收尾则 STOP 等待，不要把两份改动混在一起。除此之外的输出，逐条对照下文「现状」的**代码内容**；内容对不上即 STOP（行号漂移不算）。`pkg/telemetry` 应只多出 284 的三个文件。
>
> **2026-11-12 reconcile 更新**：上述 TUI 三文件的改动已随 `7403be84` 提交（`CurrentModelRowID` / `ComposerFooter.ModelRowID`，共 +17 行，与日志写入无关）——`git diff 17b4633a -- $SCOPE` 现在会显示这三处**已提交**差异，属预期，日志锚点未变。**新的撞车点**：`cmd/forebrain/serve.go` 当前有另一会话（gateway token 相关）的未提交改动，本计划要改它的日志装配——派发前先确认该会话已收尾并提交，否则 STOP 等待。其余范围文件对 `17b4633a` 零漂移。

## 状态

- **优先级**：P1
- **工作量**：L（约 60 个调用点，绝大多数是机械替换）
- **风险**：MED（改的是进程级日志装配；TUI 的 stderr 重定向出错会把运行时输出打到终端界面上）
- **依赖**：`plans/284-structured-event-log-foundation.md`（必须已 DONE）
- **类别**：tech-debt / bug
- **制定基线**：`17b4633a`，分支 `dev`，2026-11-11

## 为什么要做

284 建了底座但没有调用方。今天落盘日志有 9 种写入方式，互相之间格式、级别、去向都不一致，下面每一条都在 owner 机器的 `~/.forebrain/logs` 上实测过（2026-11-11）：

1. **一条恒假的警告占了 error.log 的 90%**。`pkg/run/orchestration_llm.go:821-822` 只要「流里已经跑过工具」就无条件打 `tools executed in-stream but the response reports no tool calls`——不管响应到底报没报。`error.log` 52,716 行里它占 47,286 行（44,380 条 WARN + 2,906 条 ERROR，来自这行代码的历次位置 `:682/:692/:783/:785/:805/:813/:822`），且不带会话。真正该说话的地方已经有了：`pkg/run/streaming_tools.go:335` 的 `reconcile` 只在**真的补了一条调用**时警告，并带 `tool_name` 与 `call_id`。
2. **WARN 写进 error.log**。`pkg/tui/render.go` 的 `tuiLogSink.pickFile`：`< Info → debug.log`、`< Warn → info.log`、其余（含 WARN）→ `error.log`。
3. **同一件事写两遍、三遍**。`pkg/tui/chat_session.go:2577-2578` 同一句 `forebrain chat turn error …` 先 `Errorf` 再 `Debugf`；`pkg/tui/chat_turn.go:639-641` 同一句 `turn ok` 先 `Infof` 再 `Debugf`；`Errorf` 走 `telemetry.ErrorSkip`，后者把同一条消息以 `%v`、`%#v`、`Error()` 打三遍（`pkg/telemetry/error_write.go:78-92`）；标准库 `log` 的每一行若含 `failed` / `err=` / `panic` 字样，再抄一份进 `error.log`（`logLineLooksErrory`）。
4. **级别靠猜**。上一条的关键词匹配就是级别判定；`handler_error`、`docker error` 这类字样出现在正常内容里也会被当成错误。
5. **引擎日志不带身份**。`pkg/run`、`pkg/llm`、`pkg/turn`、`pkg/assembly` 共 85 处 `slog.Xxx(`，只有 22 处手写了会话属性；全仓 325 处 `slog` 调用**没有一处**用 `slog.XxxContext`，上下文里现成的会话/轮次/项目一个都没带出来。
6. **网关只写不清**。`error.log` 由 `appendErrorFile` / `teeHandler` 每条 `os.OpenFile` 追加，轮转只有 TUI 的 2 秒定时器在做；只跑网关的部署（企业内网 K8s）`error.log` 无上限增长。
7. **网关文件镜像没有日期**。`cmd/forebrain/gateway_log.go` 把终端格式（`18:18:11.905 INF …`）原样写进 `gateway-<主机>.log`；终端不是 TTY 时又换成 logfmt。同一个文件两种格式，其中一种无法按日期查。
8. **TUI 每条日志一次 fsync，且每条新建一个 handler**（`tuiLogSink.Handle` 里 `slog.NewTextHandler(...)` + `fileSync(f)`），落在引擎线程上。
9. **`SetupEvent` 的字段顺序每次不同**（遍历 `map`），每条一次 `Sync`。

本计划把这 9 种写入方式收敛成 284 的一种，并顺手修掉 1–4、6–9；第 5 条用一条架构测试钉住。

## 现状（执行者需要的事实，全部内联）

### 284 提供的接口（已存在，直接用）

```go
telemetry.OpenEventLog(root, source string) *EventLog      // source: "tui" | "gateway" | "cli"
telemetry.SetDefaultEventLog(l) / telemetry.DefaultEventLog()
telemetry.Emit(ctx, level slog.Level, kind, msg string, attrs ...slog.Attr)
telemetry.NewEventHandler(l, level) slog.Handler
telemetry.RegisterScope(func(ctx context.Context) telemetry.Scope)   // Scope{Project, Session, Worker, Run, Agent}
telemetry.HostLabel() string
```

显式属性 `session` / `session_id` / `run` / `run_id` / `project` / `agent` / `worker` 会被提升为记录的身份字段并覆盖上下文里的值；`err` / `error` 进 `Err`。

### 进程装配点（三处）

- `cmd/forebrain/root.go:117`（`prepPersistentHome` 末尾）：
  ```go
  inner := stderrLogHandler(defaultSlogLevel(), true)
  slog.SetDefault(slog.New(telemetry.NewSlogTeeHandler(root, inner)))
  ```
- `cmd/forebrain/serve.go:57`：`restoreLog := installGatewayConsoleLog(root)`；`cmd/forebrain/gateway_log.go:61-88` 打开 `gateway-<主机>.log` 并把它作为第二个输出传给 `stderrLogHandlerTo(sink, …)`（`cmd/forebrain/console_log.go:29-45`）。
- `cmd/forebrain/interactive.go:97`：`restoreLog := tui.RedirectProcessLoggingForTUI(root)`；实现在 `pkg/tui/render.go`（`func RedirectProcessLoggingForTUI(home string) (restore func())`），它打开三个文件、把 stderr `dup2` 到 `error.log`（`pkg/tui/stderr_unix.go` / `pkg/tui/platform_windows.go` 的 `attachProcessStderrToErrorLog(logDir)`）、起 `telemetry.StartPeriodicRotation`、把 `log.SetOutput` 与 `slog.SetDefault` 指向 `tuiLogSink`。

### 身份来源（装进 `RegisterScope`）

`pkg/process/open.go:287-288` 是既有的端口装配点：
```go
llm.SetHTTPTransportWrapper(telemetry.WrapLLMHTTPTransport)
llm.SetDebugLogger(telemetry.LogProviderDebug)
```
可用的读取函数：`tool.ProjectKeyFromContext`、`tool.ConversationSessionIDFromContext`、`llm.AgentSessionIDFromContext`、`tool.RunIDFromContext`。代理名今天只有未导出的 `agentNameFromContext`（`pkg/agent/trace_context.go:54`，由 `:49` 的 `WithAgentName` 写入）。

### 要迁走的写入点

| 写入方式 | 位置 | 调用点 |
|----------|------|--------|
| `chatDiskLog`（`Infof/Debugf/Errorf/DebugForceFlushf`） | `pkg/tui/chat_session.go:2961-3011`；构造在 `pkg/tui/notify.go:1668` | 27 处，全在 `pkg/tui/chat_session.go`、`chat_turn.go`、`chat_surface.go`（`grep -n "chatLog\.\(Infof\|Debugf\|Errorf\|DebugForceFlushf\)" pkg/tui/*.go`） |
| `telemetry.LevelLogger` | `pkg/telemetry/levels.go` | 只被 `chatDiskLog` 用 |
| `telemetry.SetupEvent(home, event, fields)` | `pkg/telemetry/setup_log.go` | `pkg/process/setup.go:216`、`pkg/tui/setup.go:219/:2038/:2042` |
| `telemetry.ErrorSkip` / `ErrorThenFprintln` | `pkg/telemetry/error_write.go` | `cmd/forebrain/root.go:51`、`chatDiskLog.Errorf` |
| `telemetry.Log(root, label, recovered)`（panic） | `pkg/telemetry/paniclog.go` | `pkg/run/subagent.go:2233/:4123/:4139` |
| `telemetry.NewSlogTeeHandler` | `pkg/telemetry/slog_handler.go` | `cmd/forebrain/root.go:117`、`cmd/forebrain/gateway_log.go:82` |
| `telemetry.LogProviderDebug(topic, message)` | `pkg/telemetry/debuglog.go:71` | `pkg/process/open.go:288`、`cmd/cachebaseline/main.go:436` |
| `tuiLogSink` / `tuiStdLogWriter` / `logLineLooksErrory` | `pkg/tui/render.go` | `RedirectProcessLoggingForTUI` |
| 访问日志 | `pkg/telemetry/accesslog.go:58` `slog.Info("request", attrs...)` | 网关、MCP OAuth 监听 |

**本计划不动** `pkg/telemetry/http_log.go` 的 `httpDebugFileLogRoundTripper`、`pkg/telemetry/tool_debug.go`、`pkg/telemetry/debuglog.go` 里的 `writeDebugFileLog*`、`pkg/telemetry/logfile.go`——模型调用与工具调用的留证是 286 的范围，它们继续写 `debug.log` 直到 286 落地。

### 指向 `error.log` 的展示

- `pkg/process/runtime.go:119-122` `func ErrorLogPath(home string) string`（返回 `<home>/logs/error.log`），调用方 `pkg/gateway/slash_handlers.go:187`。
- `pkg/tui/chat_slash.go:163` `ErrorLogPath: filepath.Join(s.home(), home.LogsDir, "error.log")`。
- `pkg/turn/mcp_format.go:160/:246/:376/:597-598`：字段 `ErrorLogPath`，输出 `"\nError logs: " + inv.ErrorLogPath + "\n"`；`pkg/tui/render.go` 另有一处 `notes = append(notes, "Error logs: "+inv.ErrorLogPath)`。
- `deploy/k8s/manifests/pvc.yaml:12` 注释写「logs/debug.log is shared by every replica」。

## 需要的命令

| 用途 | 命令 | 成功时 |
|------|------|--------|
| 编译 | `CGO_ENABLED=1 go build -tags fts5 ./...` | 退出码 0 |
| 受影响包测试 | `CGO_ENABLED=1 go test -tags fts5 ./pkg/telemetry ./pkg/tui ./pkg/run ./pkg/turn ./pkg/process ./pkg/gateway ./pkg/agent ./cmd/forebrain -count=1` | 全 `ok` |
| 架构门 | `CGO_ENABLED=1 go test -tags fts5 ./pkg/architecture -count=1` | `ok` |
| vet / 格式 | `go vet ./... && gofmt -l pkg cmd` | 无输出 |
| 真机 | `.claude/skills/run-forebrain`（fake provider；隔离 home） | 见 Step 9 |

## 范围

**In scope**：Drift check 里 `SCOPE` 列出的文件；`pkg/telemetry` 下新建 `pkg/telemetry/eventfan.go`（见 Step 1）及其测试；`pkg/architecture/cache_test.go`（只追加一条测试）；以上文件对应的 `_test.go`；`pkg/run`、`pkg/turn`、`pkg/assembly`、`pkg/llm`（不含各 provider 子包）、`pkg/tool` 里把 `slog.Xxx(` 改成 `slog.XxxContext(ctx, …)` 的机械替换（Step 7）。

**Out of scope**：
- `pkg/telemetry/http_log.go`、`tool_debug.go`、`debuglog.go` 的 `writeDebugFileLog*`、`logfile.go`——286。
- `pkg/telemetry/otel.go`、`span_helpers.go`、`tool_trace.go`——OTel 链路不动。
- `pkg/tui/run.go`、`pkg/tui/input_events.go`——另一会话在改，且与日志无关。
- 终端上网关的控制台输出格式（`pkg/telemetry/console_log.go` 的渲染）——保持不变，只是不再镜像到文件。
- 任何 `pkg/gateway`、`pkg/process` 里**没有 ctx 可用**的 `slog` 调用——不要为了带身份去改函数签名。

## 步骤

### Step 1：进程装配（三处）+ 身份端口

1. `pkg/agent/trace_context.go`：新增导出函数 `func NameFromContext(ctx context.Context) string { return agentNameFromContext(ctx) }`（注释说明它是给日志身份用的只读口）。
2. `pkg/process/open.go` 在既有两行端口装配旁加：
   ```go
   // Every record names who it belongs to. telemetry cannot import tool or
   // agent (both import telemetry's callers), so the scope is read here.
   telemetry.RegisterScope(func(ctx context.Context) telemetry.Scope {
       conv := tool.ConversationSessionIDFromContext(ctx)
       worker := strings.TrimSpace(llm.AgentSessionIDFromContext(ctx))
       if worker == conv {
           worker = ""
       }
       return telemetry.Scope{Project: tool.ProjectKeyFromContext(ctx), Session: conv, Worker: worker,
           Run: tool.RunIDFromContext(ctx), Agent: agent.NameFromContext(ctx)}
   })
   ```
   若 `pkg/process` 尚未导入 `pkg/agent`，先跑 `go list -deps ./pkg/process | grep pkg/agent`；没有这条边就**不要新增**（会打红 `TestSameLayerEdgesAreEnumerated`）——改为省略 `Agent` 并在执行报告里写明，交 286 处理。
3. 新建 `pkg/telemetry/eventfan.go`：`func NewFanoutHandler(handlers ...slog.Handler) slog.Handler`——`Enabled` 任一为真即真，`Handle` 逐个调用且**一个失败不影响其它**（与 `console_log.go` 里 `MultiWriter` 的理由相同），`WithAttrs`/`WithGroup` 逐个下传。配 `eventfan_test.go`。
4. `cmd/forebrain/root.go` `prepPersistentHome`：
   ```go
   telemetry.SetDefaultEventLog(telemetry.OpenEventLog(root, "cli"))
   slog.SetDefault(slog.New(telemetry.NewFanoutHandler(
       stderrLogHandler(defaultSlogLevel(), true),
       telemetry.NewEventHandler(telemetry.DefaultEventLog(), defaultSlogLevel()))))
   ```
   `OpenEventLog` 是懒打开文件的，所以下面两处换来源不会留下空文件。
5. `cmd/forebrain/gateway_log.go`：`installGatewayConsoleLog(root)` 改为只做「把默认事件日志换成 `OpenEventLog(root, "gateway")`（先 `Close` 旧的），slog 默认 handler 用同样的 fanout」，返回的函数恢复原状并 `Close`。删除 `gatewayLogFile`、`gatewayLogPath`、`replicaLogName`（由 `telemetry.HostLabel()` 取代）。`cmd/forebrain/console_log.go` 的 `stderrLogHandlerTo(file, …)` 删掉 `file` 参数与 `MultiWriter` 分支，只留写 stderr 的两种格式。
6. `pkg/tui/render.go` `RedirectProcessLoggingForTUI(home)` 重写为：
   - 默认事件日志换成 `OpenEventLog(home, "tui")`；
   - `slog.SetDefault(slog.New(telemetry.NewEventHandler(log, slogLevelForTUILogFile())))`（TUI 的 stderr 是界面，不 fanout）；
   - `log.SetOutput(...)` 接一个把每行写成 `INFO`、`kind=log`、属性 `via=stdlog` 的小写入器（**不再**做关键词判级，不再抄送）；
   - stderr 仍然 `dup2` 到文件，文件改为 `<home>/logs/<今天>/stderr-tui-<主机>-<pid>-<随机>.log`（`attachProcessStderrToErrorLog` 改名 `attachProcessStderr(path string)`，Unix 与 Windows 两个实现都改；路径由调用方用 `EventLog.BlobPath` 同目录规则拼，不放进 `x/`）。运行时崩溃输出无法结构化，所以它保持原始文本，287 的读取方把它当一条记录展示；
   - 删除 `tuiLogSink`、`tuiStdLogWriter`、`logLineLooksErrory`、`closeFiles`、`openFile`/`fileWrite`/`fileSync`/`fileClose` 这些只为旧路径存在的包级变量，以及 `telemetry.StartPeriodicRotation` 调用。
   - `home` 为空或目录建不出来时，保持今天的 `redirectLoggingToDiscard()` 行为。

**验证**：`CGO_ENABLED=1 go build -tags fts5 ./... && CGO_ENABLED=1 go test -tags fts5 ./pkg/telemetry ./cmd/forebrain -count=1` → 退出码 0、`ok`（`cmd/forebrain/console_log_test.go` 里断言文件镜像的用例改为断言事件文件里有对应记录）。

### Step 2：`chatDiskLog` → `telemetry.Emit`

删除 `pkg/tui/chat_session.go` 的 `chatDiskLog` 类型、`newChatDiskLog`、字段 `chatLog *chatDiskLog`（`:81`）与 `pkg/tui/notify.go:1668` 的构造，并删除 `pkg/telemetry/levels.go`（及 `levels_test.go`，若存在）。27 个调用点按下表改写；规则：**消息是不含变量的短语，变量全部进属性**，身份属性名固定用 `session` / `run`。

| 原调用（按文件内出现顺序） | 新级别 | kind | 新消息 | 属性 |
|---|---|---|---|---|
| `chat_session.go` `tool_approval ui_submit …`（`DebugForceFlushf`） | DEBUG | log | `tool approval submitted` | `approved, cancelled, key_chord, action_id, policy_kind, reason, detail_preview` |
| `dangling tool_calls repair failed`（两处） | WARN | log | `dangling tool calls repair failed` | `session, err` |
| `withdraw failed` | ERROR | log | `withdraw failed` | `session, row_id, err` |
| `turn start …` | INFO | turn | `turn started` | `session, channel, input_bytes, preview` |
| `repaired dangling tool_calls` | INFO | log | `dangling tool calls repaired` | `session, rows` |
| `auto compact skipped` | DEBUG | log | `auto compact skipped` | `session, err` |
| `bind user row … failed` | ERROR | log | `bind user row failed` | `session, row_id, run, err` |
| `turn awaiting approval` | INFO | turn | `turn awaiting approval` | `session, run, action_id` |
| `withdraw refused-turn row` | ERROR | log | `withdraw refused-turn row failed` | `session, row_id, err` |
| `turn cancelled` | INFO | turn | `turn cancelled` | `session, elapsed` |
| `turn skill load failed` | ERROR | turn | `turn failed` | `session, skill, elapsed, err, reason=skill_load` |
| `turn error`（`Errorf` + `Debugf` 两行） | ERROR | turn | `turn failed` | `session, elapsed, err`——**两行合成一条** |
| `chat_surface.go` `tool_approval clear_context failed` | WARN | log | `tool approval clear context failed` | `session, err` |
| `tool_approval … dispatch failed after N attempts` | ERROR | log | `tool approval dispatch failed` | `label, attempts, action_id` |
| `plan_review deliver failed` | ERROR | log | `plan review deliver failed` | `action_id, err` |
| `chat_turn.go` `append_assistant_outcome … failed` | ERROR | log | `append assistant outcome failed` | `session, run, err` |
| `turn ok`（`Infof` + `Debugf` 两行） | INFO | turn | `turn completed` | `session, assistant_bytes, elapsed, preview`——**两行合成一条** |
| `resumeAfterApproval skipped` | DEBUG | log | `resume after approval skipped` | `action_id` |
| `chat <label> session=…`（`:958`） | DEBUG | turn | `turn resumed` | `label, session, run, action_id` |
| `chat <label> cancelled` | INFO | turn | `turn cancelled` | `label, session, elapsed` |
| `chat <label> error` | ERROR | turn | `turn failed` | `label, session, elapsed, err` |
| `resumeAfterDenial skipped` / `resumeAfterRejectedStop skipped` | DEBUG | log | `resume after denial skipped` / `resume after rejected stop skipped` | `action_id` |
| `tool_approval abort` | DEBUG | log | `tool approval aborted` | `action_id, run, cancel_active` |

- `err` 属性直接传 `error` 值（不要先 `fmt.Sprintf`），284 会展开错误链。
- 有 `ctx` 的地方传它；没有的传 `context.Background()` 并靠显式 `session` / `run` 属性。
- 原先每处的 `if s.chatLog != nil` 守卫全部删除（`Emit` 自带空日志保护）。

**验证**：`grep -rn "chatLog\|chatDiskLog\|LevelLogger" pkg cmd --include='*.go'` → 无输出；`CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -count=1` → `ok`（`pkg/tui/chat_session_test.go` 里读 `info.log` / `debug.log` 的断言改为读事件文件；给测试加一个包内小助手 `readEvents(t, home) []map[string]any`，遍历 `logs/*/tui-*.jsonl`）。

### Step 3：删掉恒假的警告

`pkg/run/orchestration_llm.go` 当前内容：

```go
		if exec != nil && exec.dispatched() && res != nil && res.Message != nil {
			slog.Warn("tools executed in-stream but the response reports no tool calls")
			res.Message.ToolCalls = exec.reconcile(res.Message.ToolCalls)
		}
```

删掉 `slog.Warn(...)` 那一行，其余不动（上方注释保留）。`reconcile`（`pkg/run/streaming_tools.go:320-341`）在真的补调用时自己警告，把它那句改成 `slog.WarnContext(ctx, …)` 需要 `ctx` 参数——`reconcile` 没有 `ctx`，**不要改签名**，保持 `slog.Warn`。

先写复现再删：在 `pkg/run/orchestration_llm_test.go` 加 `TestStreamedToolsReportedByTheResponseAreNotWarned`——装一个记录型 `slog.Handler`（照同文件里既有的日志捕获写法；没有就用 `slog.New(slog.NewTextHandler(&buf, nil))` 临时替换默认并 `t.Cleanup` 还原），驱动「流里跑了一个工具、最终响应也报了同一个调用」的一轮（照同文件里 `in-stream` / `streaming` 相关既有用例的脚手架），断言捕获内容**不含** `reports no tool calls` 也不含 `outran the response`。删行前此测试红，删行后绿。再加一条 `TestStreamedToolMissingFromTheResponseIsWarnedOnce`：最终响应少报一个调用，断言 `outran the response's tool calls` 恰好出现 1 次且带 `call_id`。

**验证**：`CGO_ENABLED=1 go test -tags fts5 ./pkg/run -run 'TestStreamedTool' -count=1` → `ok`；`grep -rn "reports no tool calls" pkg --include='*.go' | grep -v _test.go` → 无输出。

### Step 4：`SetupEvent`、错误、panic 的函数体

保留三个函数的**签名**（调用点不用改），只换函数体：

- `telemetry.SetupEvent(home, event, fields)`：`Emit(context.Background(), slog.LevelInfo, "setup", sanitizeToken(event), attrs…)`，属性由 `fields` 按键名**排序**后生成，值仍过 `sanitizeValue`（键名含 `key`/`secret`/`token` 的值写 `[REDACTED]`，`_set`/`_present`/`has_` 结尾或开头的照写）。`home` 参数保留但不再用于拼路径；默认事件日志为空时什么都不做。删除文件写入与 `Sync`。
- `telemetry.ErrorSkip(root, err, context, extraFrames)` / `ErrorThenFprintln`：写一条 `ERROR`、`kind=log`、`Msg=context`（空则 `error`）、`Err=err`、`At` 用 `runtimeCaller(2+extraFrames)` 算出的位置。删除 `appendErrorFile`、`writeErrorEntry`、`formatErrorDetails`（确认无其它引用后）。
- `telemetry.Log(root, label, recovered)`：写一条 `ERROR`、`kind=panic`、`Msg="recovered: "+label`、`Err=fmt.Sprint(recovered)`、属性 `stack=string(debug.Stack())`（284 对键名 `stack` 的单属性上限是 32KB）。删除 `appendFile`、`formatRecovered`、`writeMu`。
- 删除 `pkg/telemetry/slog_handler.go`（`NewSlogTeeHandler` 已无调用方）及其测试。
- `telemetry.LogProviderDebug(topic, message)`：函数体改为 `Emit(context.Background(), slog.LevelDebug, "log", topic, slog.String("detail", message))`，仍受 `debugFileLogEnabled()` 开关控制。

**验证**：`CGO_ENABLED=1 go test -tags fts5 ./pkg/telemetry -count=1` → `ok`（`setup_log_test.go`、`paniclog_test.go`、`debuglog_test.go` 里读旧文件的断言改为读事件文件；断言语义不变：脱敏、位置、栈都在）。`grep -rn '"error.log"\|"info.log"' pkg/telemetry --include='*.go' | grep -v _test.go` → 只剩 `eventlog.go` 清理旧文件的那一处模式。

### Step 5：访问日志分级

`pkg/telemetry/accesslog.go` 当前：

```go
			if recovered != nil {
				slog.Error("request panicked", append(attrs, "panic", recovered)...)
				panic(recovered)
			}
			slog.Info("request", attrs...)
```

改为按结果分级并标类型（用 `Emit` 之外再保留 slog 调用会写两条，所以**只走 slog**，类型靠一个保留属性）：在 `attrs` 前面加 `"kind", "http"`（284 的 `NewEventHandler` 会把属性键 `kind` 提升为记录的 `Kind`）。级别：

| 结果 | 级别 |
|------|------|
| panic、状态码 ≥ 500 | ERROR |
| 状态码 400–499 | WARN |
| 其余且方法是 GET / HEAD / OPTIONS | DEBUG |
| 其余 | INFO |

用 `slog.Log(r.Context(), level, "request", attrs...)`。理由：网页每次打开会话是十几个成功的 GET、心跳轮询每几秒一条，它们今天全是 INFO，把真正的 INFO 淹掉。

**验证**：`CGO_ENABLED=1 go test -tags fts5 ./pkg/telemetry -run 'TestAccessLog' -count=1` → `ok`（既有用例里断言 `INFO` 的 GET 请求改为 `DEBUG`，新增 4xx→WARN、5xx→ERROR、POST 2xx→INFO 三条）。

### Step 6：展示里的 `error.log` 路径

- `pkg/process/runtime.go`：`ErrorLogPath` 改名 `LogsDirPath(home string) string`，返回 `<home>/logs`。
- `pkg/turn/mcp_format.go`：字段 `ErrorLogPath` 改名 `LogsDir`（两个结构体 + `:376` 的赋值），输出文案 `"\nLogs: " + inv.LogsDir + "\n"`；`pkg/tui/render.go` 那一处同样改成 `"Logs: "+inv.LogsDir`。
- `pkg/tui/chat_slash.go:163`、`pkg/gateway/slash_handlers.go:187` 改用新名字。
- `deploy/k8s/manifests/pvc.yaml:12` 的注释改为：`logs/ holds one file per replica process (logs/<date>/gateway-<pod>-<pid>-<id>.jsonl); no file is shared between replicas.`

**验证**：`grep -rn "ErrorLogPath\|Error logs:" pkg cmd --include='*.go'` → 无输出；`CGO_ENABLED=1 go test -tags fts5 ./pkg/turn ./pkg/gateway ./pkg/process -count=1` → `ok`。

### Step 7：引擎日志带上下文 + 架构测试

1. `pkg/architecture/cache_test.go` 追加 `TestEngineLogsCarryContext`：用 `go/parser` 遍历 `pkg/run`、`pkg/turn`、`pkg/assembly`、`pkg/llm`（只这一层，不含子目录）、`pkg/tool` 的非测试文件；对每个函数声明或函数字面量，若它的**参数表里有类型为 `context.Context` 的参数**，则函数体内（不深入内层函数字面量，内层按它自己的参数表判）出现 `slog.Debug(` / `slog.Info(` / `slog.Warn(` / `slog.Error(` 即违规，报 `文件:行`。写法照同文件 `TestNoDuplicateImportsWithinAFile` 的遍历骨架。
2. 先跑它拿到违规清单，再逐条把 `slog.X(` 改成 `slog.XContext(<那个参数名>, `。**只改这一种形状**，不要为没有 ctx 的函数加参数。

**验证**：`CGO_ENABLED=1 go test -tags fts5 ./pkg/architecture -run TestEngineLogsCarryContext -count=1` → `ok`；`CGO_ENABLED=1 go test -tags fts5 ./pkg/run ./pkg/turn ./pkg/assembly ./pkg/llm ./pkg/tool -count=1` → 全 `ok`。

### Step 8：全量回归

**验证**：`CGO_ENABLED=1 go build -tags fts5 ./... && go vet ./... && gofmt -l pkg cmd`（无输出）；「需要的命令」表里的受影响包测试与架构门全绿。

### Step 9：真机验收（fake provider，隔离 home）

按 `.claude/skills/run-forebrain` 的配方起一个 TUI（`FOREBRAIN_HOME=$(mktemp -d)/home`，fake provider），发一条消息跑完一轮后退出，再用同一个 home 起一次网关并请求一次 `/api/runtime`。逐条核对：

```sh
H=<隔离 home>
ls "$H/logs"                                   # 只有一个日期目录；没有 info.log / error.log / gateway-*.log
ls "$H/logs"/*/                                # tui-*.jsonl、gateway-*.jsonl、stderr-tui-*.log（debug.log 仍在根下，属 286）
jq -c 'select(.kind=="turn")|[.lvl,.msg,.session]' "$H"/logs/*/tui-*.jsonl     # 有 turn started / turn completed，session 非空
jq -c 'select(.session==null and .kind=="turn")' "$H"/logs/*/tui-*.jsonl | wc -l  # 0
grep -c "reports no tool calls" "$H"/logs/*/*.jsonl                              # 0
jq -c 'select(.kind=="http")|.lvl' "$H"/logs/*/gateway-*.jsonl | sort | uniq -c    # GET 200 是 DEBUG
jq -e . "$H"/logs/*/*.jsonl > /dev/null && echo all-valid-json                     # all-valid-json
```

fake provider 驱动不了的项写进执行报告的「owner 手动验收」清单，不要自行判定通过。

## 测试计划

- `pkg/telemetry/eventfan_test.go`：一个 handler 返回错误时另一个仍收到记录；`WithAttrs`/`WithGroup` 下传。
- `pkg/telemetry/accesslog_test.go`：分级四条 + `kind=http`。
- `pkg/telemetry/setup_log_test.go`、`paniclog_test.go`、`debuglog_test.go`：断言改读事件文件，另加「`SetupEvent` 两次调用属性顺序一致」。
- `pkg/run/orchestration_llm_test.go`：Step 3 的两条。
- `pkg/tui/chat_session_test.go`：一轮成功只有**一条** `turn completed`；一轮失败只有**一条** `turn failed`（原来各两条）。
- `pkg/tui/render_test.go`（或既有覆盖 `RedirectProcessLoggingForTUI` 的测试文件）：WARN 级 slog 记录的 `lvl` 是 `WARN`；标准库 `log.Printf("x failed")` 得到一条 `INFO`、`via=stdlog`，且**没有**第二条 ERROR。
- `pkg/architecture/cache_test.go`：`TestEngineLogsCarryContext`。

## 完成判据（全部满足）

- [ ] `CGO_ENABLED=1 go build -tags fts5 ./...` 退出码 0；`go vet ./...`、`gofmt -l pkg cmd` 无输出
- [ ] 「需要的命令」表里的受影响包测试与架构门全 `ok`
- [ ] `grep -rn "chatDiskLog\|LevelLogger\|NewSlogTeeHandler\|tuiLogSink\|logLineLooksErrory\|StartPeriodicRotation(" pkg cmd --include='*.go' | grep -v "pkg/telemetry/logfile"` 无输出
- [ ] `grep -rn "reports no tool calls" pkg --include='*.go' | grep -v _test.go` 无输出
- [ ] `grep -rnE '"(info|error)\.log"|gateway-.*\.log' pkg cmd --include='*.go' | grep -v _test.go | grep -v "pkg/telemetry/eventlog.go"` 无输出
- [ ] Step 9 的七条核对全部符合，输出贴进执行报告
- [ ] 改动文件 ⊆ 「范围」；`plans/README.md` 状态行已更新

## STOP 条件

- 284 未完成（`pkg/telemetry/eventlog.go` 不存在或其测试不过）。
- `pkg/tui/render.go` / `chat_slash.go` / `notify.go` 仍有另一会话的未提交改动且对方未收尾。
- Step 1 第 2 点：`pkg/process` → `pkg/agent` 不是既有导入边（按该点写明的退路处理并报告，不要新增边）。
- Step 7 的违规清单超过 120 条（说明规则写宽了或范围理解有误，先报告清单）。
- TUI 起来后终端界面上出现原始日志文字（stderr 重定向坏了）：立刻回退 Step 1 第 6 点并报告。
- 任一步需要改 `pkg/telemetry/http_log.go` / `tool_debug.go` 才能编译。

## 非功能要求

| 维度 | 风险 | 对策 | 验证 |
|------|------|------|------|
| 延迟 | 引擎线程上的日志开销 | 去掉每条一次的 fsync 与每条新建 handler；284 每条一次 `write(2)` | Step 9 跑一轮不卡顿；`pkg/tui` 既有性能相关测试不退化 |
| 鲁棒 | 日志装配失败拖垮启动 | `OpenEventLog` 不返回错误；`home` 不可用时沿用 `redirectLoggingToDiscard` | `pkg/tui` 既有「日志目录不可写」用例保持通过 |
| 鲁棒 | 运行时崩溃输出丢失 | stderr 仍是内核级 `dup2` 到文件，不经用户态转发 | Step 9 `ls` 能看到 `stderr-tui-*.log` |
| 可维护 | 新代码又不带身份 | `TestEngineLogsCarryContext` | 架构门 |
| 可维护 | 第二套日志语义回潮 | 完成判据的三条 grep | — |
| 体验 | 终端里习惯 `tail -f debug.log` 的人 | 记录以 `{"t":…,"lvl":…,"kind":…,"msg":…` 开头，`tail -f … \| jq -c '[.t,.lvl,.msg]'` 可读；网页见 288 | owner 手动验收 |

## 维护注意

- 这之后新增日志只有两种写法：`slog.XxxContext(ctx, …)`（普通日志）与 `telemetry.Emit(ctx, level, kind, …)`（有类型的事件）。不要再开文件。
- 访问日志的分级是有意的行为变化：终端上成功的 GET 现在显示为 `DBG`。
- `Logs: <目录>` 替换了 `Error logs: <文件>`：TUI 与网页的 MCP 面板文案随之变化（英文原话，引擎单一来源）。
- 286 落地前，`debug.log` 仍由模型调用与工具调用的旧路径写入，这是预期的中间状态。
- 评审重点：Step 2 的 27 处是否真的「两行合成一条」；Step 1 第 6 点 Unix 与 Windows 两个 stderr 实现是否都改了；有没有顺手改了没有 ctx 的函数签名（不允许）。
