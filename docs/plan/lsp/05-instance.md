# 任务 05：服务器实例——进程、环境、初始化、服务器请求、就绪、重启与进程树

> **执行者须知**：逐步执行；每一步运行「验证」命令并确认结果后再进入下一步。出现「STOP 条件」中的任何情况立即停止并汇报。完成后把 `docs/plan/lsp/README.md` 中本任务状态改为 `DONE`。
>
> **规范**：`docs/plan/LSP_CODE_INTELLIGENCE_PLAN.md`。开始前读规范 §3.4、§3.5、§3.14、§5.4（`logs/`、`pids.json`）、§6.2 末尾（`${LSP_CACHE_DIR}`）、§7.1–§7.3、§7.7、§7.8、§9.3、附录 A。
>
> **前置**：任务 04 已合入（`pkg/lsp/jsonrpc.go`、`protocol.go`、`position.go` 存在）。
>
> **漂移检查（先运行）**：`git diff --stat 6305ea9..HEAD -- pkg/home/envguard.go pkg/mcp/stdio_secure.go pkg/mcp/bridge_test.go`。有输出时对比「现状」摘录。

## 状态

- 优先级：P0 · 工作量：L · 风险：MED（子进程生命周期；测试要跨平台）
- 依赖：04
- 类别：direction
- 计划基于：`6305ea9`，2026-09-29

## 为什么

一个「实例」就是一个运行中的语言服务器进程加上它的 JSON-RPC 连接。本任务负责：用受控的环境变量启动进程、完成 `initialize` 握手、应答服务器发来的请求（配置、进度、对话框、编辑请求）、判断就绪、崩溃后按规则重启、关闭时清理整棵进程树，以及日志落盘。之后的任务只通过 `Instance` 的方法与服务器交互。

## 现状

- `pkg/home/envguard.go:27 SafeSubprocessEnv(parent []string, opts Options) []string`：只保留 `defaultAllow`（`PATH HOME TMPDIR TMP TEMP LANG LC_ALL TZ USER SHELL TERM`）里的父进程变量；`opts.ExplicitEnv` 中名字只含 `[A-Za-z0-9_]` 且不像密钥的变量会被加入（`:56-75`）。
- `pkg/home` 有 `DefaultEnvPath(home string) string`（`~/.forebrain/.env` 的路径，`pkg/mcp/stdio_secure.go:80` 用它）。
- `pkg/mcp/stdio_secure.go:60-75 envLookupForServer`：全局条目从进程环境展开 `${VAR}`，项目条目只从 `~/.forebrain/.env`（用 `github.com/joho/godotenv` 解析）展开。LSP 照同样规则。
- `pkg/mcp/bridge_test.go:398-412 buildProcessFixture`：把 Go 源码字符串写到临时目录，`go build` 成可执行文件（`cmd.Dir` = 仓库根，所以源码可以导入本模块的包）；`:421 waitForProcessGone` 用 `ps -o state=` 断言进程已消失；`:447 processFixtureSource` 是源码常量的写法。
- 结构约束：`pkg/lsp` 目前 8 个生产文件，本任务后 11 个；**不能**把假服务器放进 `testdata/*.go`（结构测试会把它当包）。

## 范围

**新建：** `pkg/lsp/instance.go`、`pkg/lsp/procgroup_unix.go`、`pkg/lsp/procgroup_windows.go`、`pkg/lsp/instance_test.go`。**修改：** `pkg/lsp/doc.go`（文件清单）、`pkg/architecture/testdata/graph.json`（重新生成：`pkg/lsp` 开始导入 `pkg/home`）。

**不要碰：** `pkg/home`、`pkg/mcp`、`pkg/lsp` 的其他文件（除 `doc.go`）。

## 步骤

### 步骤 1：`procgroup_unix.go`（`//go:build unix`）与 `procgroup_windows.go`（`//go:build windows`）

两个文件提供同一组**未导出**函数：

```go
// prepareCommand configures cmd so its whole process tree can be signalled.
func prepareCommand(cmd *exec.Cmd)

// processTree is a started server's process tree.
type processTree struct { /* unix: pgid int; windows: job windows.Handle */ }

// attachTree is called right after cmd.Start.
func attachTree(cmd *exec.Cmd) (*processTree, error)

func (t *processTree) terminate() error // unix: SIGTERM to -pgid; windows: TerminateJobObject
func (t *processTree) kill() error      // unix: SIGKILL to -pgid; windows: TerminateJobObject
func (t *processTree) release()         // windows: CloseHandle(job); unix: no-op

// processStarted identifies a process start for orphan checks ("" when unknown).
func processStarted(pid int) string

// sweepOrphans kills recorded process groups left by a previous forebrain
// that died without shutting its servers down, then removes their records.
func sweepOrphans(pidFile string)
```

- Unix：`prepareCommand` 设 `cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}`；`attachTree` 记 `pgid = cmd.Process.Pid`；信号用 `syscall.Kill(-pgid, sig)`，`ESRCH` 视为成功；`processStarted` 执行 `ps -o lstart= -p <pid>`（2 秒超时）取去空白后的输出。
- Windows（用已有依赖 `golang.org/x/sys/windows`）：`attachTree` 里 `CreateJobObject(nil, nil)` → `SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation, …)`，`LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE` → `OpenProcess(PROCESS_SET_QUOTA|PROCESS_TERMINATE, false, pid)` → `AssignProcessToJobObject`；`processStarted` 返回 `""`；`sweepOrphans` 为空实现（Job Object 在 forebrain 退出时自动杀掉子进程树）。
- `pids.json` 的读写（Unix 实现）：内容为 `[{"server":"gopls","pid":123,"pgid":123,"started":"Tue Sep 29 10:00:00 2026"}]`；`recordPID(pidFile, rec)`、`forgetPID(pidFile, pid)` 两个未导出函数放在 `procgroup_unix.go`，Windows 版为空实现；写入用「临时文件 + rename」，并发访问用包级互斥。`sweepOrphans` 对每条记录：`processStarted(pid) == rec.Started` 且非空时 `syscall.Kill(-pgid, SIGKILL)`；最后写回空数组。

**验证**：`CGO_ENABLED=1 go build -tags fts5 ./pkg/lsp` → 退出码 0；`GOOS=windows GOARCH=amd64 go vet ./pkg/lsp` → 退出码 0；`GOOS=darwin GOARCH=arm64 go vet ./pkg/lsp` → 退出码 0。

### 步骤 2：`instance.go` —— 环境与占位符

```go
// EnvSpec describes a server process environment (spec §9.3).
type EnvSpec struct {
	Passthrough []string          // parent variables allowed through
	Env         map[string]string // explicit variables; values may reference ${VAR}
	FromProject bool              // ${VAR} resolves only from ~/.forebrain/.env
	Home        string            // FOREBRAIN_HOME
}

// BuildEnv returns the environment a server process runs with, and the
// ${VAR} references that did not resolve.
func BuildEnv(spec EnvSpec) (env []string, missing []string)

// CacheDirPlaceholder is the only placeholder the lsp layer expands (spec §6.2).
const CacheDirPlaceholder = "${LSP_CACHE_DIR}"

// ExpandCacheDir replaces CacheDirPlaceholder in s.
func ExpandCacheDir(s, cacheDir string) string

// ExpandCacheDirJSON replaces CacheDirPlaceholder in every string value of a JSON document.
func ExpandCacheDirJSON(raw json.RawMessage, cacheDir string) json.RawMessage
```

- `BuildEnv`：`explicit := map[string]string{}`；对 `Passthrough` 里每个名字，`os.LookupEnv` 有值则放入；再放入 `Env`（值里的 `${NAME}` 用查找函数展开：`FromProject` 时只查 `godotenv.Read(home.DefaultEnvPath(spec.Home))` 的结果，否则查 `os.LookupEnv`；找不到的名字记入 `missing` 并替换为空串；`${LSP_CACHE_DIR}` 不在这里展开）；最后 `home.SafeSubprocessEnv(nil, home.Options{ExplicitEnv: explicit})`。
- `ExpandCacheDirJSON`：解码为 `any`，递归替换所有字符串，重新编码；输入非法或为空时原样返回。

### 步骤 3：`instance.go` —— 实例

```go
type InstanceState string

const (
	StateStarting     InstanceState = "starting"
	StateInitializing InstanceState = "initializing"
	StateIndexing     InstanceState = "indexing"
	StateReady        InstanceState = "ready"
	StateFailed       InstanceState = "failed"
	StateStopped      InstanceState = "stopped"
)

// Readiness strategies (spec §7.7).
const (
	ReadinessProgress           = "progress"
	ReadinessRustAnalyzerStatus = "rust-analyzer-status"
	ReadinessJDTLSStatus        = "jdtls-status"
	ReadinessNone               = "none"
)

// InstanceSpec is everything needed to start one server process.
type InstanceSpec struct {
	ServerID              string
	Command               string // absolute path or a name looked up on the PATH in Env
	Args                  []string
	Env                   []string
	Root                  string // absolute: the process directory, rootUri and first workspace folder
	InitializationOptions json.RawMessage
	Settings              json.RawMessage
	AutoAnswers           map[string]string
	Readiness             string
	StartupTimeout        time.Duration // 0 means 60s
	ShutdownTimeout       time.Duration // 0 means 5s
	RestartOnCrash        bool
	MaxRestarts           int // restarts allowed in a sliding 10-minute window
	LogPath               string // "" discards logs
	PIDFile               string // "" skips orphan bookkeeping
	// OnNotification receives every server notification after the instance
	// handled its own ones ($/progress, status). Must not block.
	OnNotification func(method string, params json.RawMessage)
	// OnStateChange is called after every state or progress change. Must not block.
	OnStateChange func()
	// OnRestart is called after a crash restart finished initializing, before
	// new calls are let through (the document layer re-opens its documents).
	OnRestart func(ctx context.Context)
}

type Instance struct { /* unexported */ }

// StartInstance starts the process and completes initialize within
// StartupTimeout. It returns an error (and leaves nothing running) when the
// command is missing, the process exits, or initialize does not complete.
func StartInstance(ctx context.Context, spec InstanceSpec) (*Instance, error)

func (i *Instance) Call(ctx context.Context, method string, params, result any) error
func (i *Instance) Notify(method string, params any) error
func (i *Instance) Capabilities() ServerCapabilities
func (i *Instance) Encoding() Encoding
func (i *Instance) State() InstanceState
func (i *Instance) Progress() int // highest percentage among active progress tokens; -1 when none reported
func (i *Instance) WaitReady(ctx context.Context) error
func (i *Instance) LastError() string
func (i *Instance) PID() int
func (i *Instance) Folders() []string
func (i *Instance) AddFolder(ctx context.Context, root string) error // sends didChangeWorkspaceFolders
func (i *Instance) Registrations(method string) []json.RawMessage     // registerOptions of live client/registerCapability entries for method
func (i *Instance) Restart(ctx context.Context) error                // manual: resets the crash window
func (i *Instance) Shutdown(ctx context.Context) error               // graceful, then tree kill; idempotent
func (i *Instance) Done() <-chan struct{}                            // closed when stopped or failed for good
```

实现规则：

1. **启动**：`exec.LookPath` 用 `Env` 里的 `PATH`（从 `Env` 取，找不到用进程的）解析 `Command`；找不到返回 `fmt.Errorf("command %q not found", Command)`。`cmd.Dir = Root`，`cmd.Env = Env`，`prepareCommand(cmd)`，stdin/stdout 用管道，stderr 写日志（`LogPath` 为空时丢弃；**绝不**用 `os.Stderr`）。`Start` 后 `attachTree`，有 `PIDFile` 时 `recordPID`。
2. **日志**：`LogPath` 所在目录 `MkdirAll 0o755`；打开前若文件 > 5 MiB，重命名为 `<LogPath>.1`（覆盖旧的）；追加写；每行加 RFC3339 时间戳前缀；`window/logMessage`、`window/showMessage`、自动应答的对话框、被拒绝的 `workspace/applyEdit` 都写进来。
3. **初始化**：`Call("initialize", params)`，`params` 为 `{"processId": os.Getpid(), "clientInfo": {"name": "forebrain", "version": "dev"}, "locale": "en", "rootPath": Root, "rootUri": PathToURI(Root), "workspaceFolders": [{"uri", "name": filepath.Base(Root)}], "initializationOptions": <raw 或省略>, "capabilities": <clientCapabilitiesJSON>, "trace": "off"}`；结果里的 `capabilities` 存为 `ServerCapabilities`，`Encoding = ParseEncoding(caps.PositionEncoding())`；发 `initialized {}`；`Settings` 非空时发 `workspace/didChangeConfiguration {"settings": <Settings>}`。整个过程受 `StartupTimeout` 约束，超时则 `tree.kill()` 并返回 `fmt.Errorf("initialize did not complete within %s", …)`。
4. **`clientCapabilitiesJSON`**：未导出字符串常量，内容**逐字**等于规范附录 A 的 JSON（可以去掉缩进空白，但字段与值完全一致）；测试用 `json.Valid` 与字段断言校验。
5. **服务器请求**（`ConnOptions.OnRequest`），逐条照规范 §7.3：
   - `workspace/configuration`：把 `Settings` 解码为 `map[string]any`，对每个 item 的 `section` 按 `.` 分段逐层查找，找不到为 `nil`，`section == ""` 返回整个 settings；`section == "python"` 且结果（对象或 nil）中没有 `pythonPath` 时，按规范顺序探测解释器（`$VIRTUAL_ENV` 从 `Env` 取），找到则在结果对象里设 `pythonPath`。
   - `client/registerCapability`、`client/unregisterCapability`：记录 `Registration`（id → method + options；unregister 按 id 删除），回 `nil`。`Registrations(method)` 返回当前仍有效、`method` 相同的各条 `RegisterOptions`。
   - `window/workDoneProgress/create`：回 `nil`。
   - `window/showMessageRequest`：消息文本（小写）包含某个 `AutoAnswers` 键（小写）时，在 `actions` 中找标题等于该值的项并返回 `MessageActionItem`；否则回 `nil`。写日志。
   - `window/showDocument`：回 `{"success": false}`。
   - `workspace/applyEdit`：回 `{"applied": false, "failureReason": "read-only client"}`，写日志。
   - `workspace/workspaceFolders`：回当前 folders（`[]WorkspaceFolder`）。
   - `workspace/diagnostic/refresh`：置内部标记 `pullStale`（任务 07 读），回 `nil`。
   - 方法名以 `/refresh` 结尾的其他请求：回 `nil`。
   - 其他：返回 `ErrMethodNotFound`。
6. **通知**（`ConnOptions.OnNotify`，必须快速返回）：`$/progress` 更新 token 表（`begin` 加入、`report` 更新百分比、`end` 移除）；`window/logMessage` / `window/showMessage` 写日志，`type == 1` 时更新 `LastError`；`experimental/serverStatus` 的 `quiescent == true` 标记就绪信号、`health == "error"` 更新 `LastError`；`language/status` 的 `type == "ServiceReady"` 标记就绪信号、`"Error"` 更新 `LastError`；处理后把所有通知转给 `OnNotification`（若非 nil）。
7. **就绪**：`initialized` 之后状态为 `indexing`，按 `Readiness` 转为 `ready`：`none` 立即；`progress` 在没有活动 token 的状态持续 `quietPeriod`（包级变量，默认 500ms，测试可改）；`rust-analyzer-status` 收到 quiescent；`jdtls-status` 收到 ServiceReady。`WaitReady` 在 `ready` 返回 nil，在 `failed`/`stopped` 返回错误，ctx 结束返回 `ctx.Err()`。每次状态或进度变化调 `OnStateChange`。
8. **崩溃与重启**：一个 goroutine 等待 `cmd.Wait()`。非 `Shutdown` 引起的退出视为崩溃：`LastError` 记为 `exited: <err>` 加日志最后一行；所有待决调用失败，错误文本 `language server <id> restarted`（未能重启时为 `language server <id> stopped: <reason>`）。若 `RestartOnCrash` 且过去 10 分钟内的崩溃次数 ≤ `MaxRestarts`：按 `restartBackoff`（包级变量，默认 `1s, 2s, 4s`，超过长度用最后一个）等待后重新走步骤 1–3（新进程、新连接），成功后调 `OnRestart`，再放行新调用；重启期间到来的 `Call` 等待至 `ready`/`indexing` 或失败或 ctx 结束。否则状态 `failed`，`LastError = fmt.Sprintf("crashed %d times in 10 minutes", n)`，关闭 `Done()`。
9. **`Restart`**：清零崩溃窗口，优雅关闭当前进程（不计为崩溃），重新启动并初始化，调 `OnRestart`。
10. **`Shutdown`**：状态置 `stopped`（后续 `Call` 立即返回错误）；`Call("shutdown")`（超时 `ShutdownTimeout`）→ `Notify("exit")` → 最多等 1 秒进程退出 → `tree.terminate()` → 最多等 2 秒 → `tree.kill()` → 等 `Wait` 返回 → `tree.release()` → `forgetPID` → 关闭日志 → 关闭 `Done()`。可重复调用。
11. **`AddFolder`**：未包含时追加到 folders 并发 `workspace/didChangeWorkspaceFolders {"event": {"added": [folder], "removed": []}}`。

### 步骤 4：假服务器与测试辅助（`instance_test.go`）

在 `instance_test.go` 中：

1. 常量 `fakeServerSource`：一个 `package main` 程序（可 import `github.com/forebrain-harness/forebrain-harness/pkg/lsp` 以复用 `ReadMessage`/`WriteMessage`），从环境变量 `FAKE_LSP_SCRIPT` 读取 JSON 脚本，支持以下字段（全部可选）：

   | 字段 | 行为 |
   |---|---|
   | `capabilities` | `initialize` 结果的 `capabilities` 原样返回 |
   | `init_delay_ms` / `never_initialize` | 延迟回应 / 永不回应 `initialize` |
   | `record` | 路径：把收到的每条消息与每个服务器请求得到的回应，每行一个 JSON 追加写入 |
   | `after_initialized` | 数组：收到 `initialized` 后依次执行，每项为 `{"request": 方法, "params": …}`（发给客户端并记录回应）或 `{"notify": 方法, "params": …, "delay_ms": n}` |
   | `responses` | 方法 → 固定结果 JSON |
   | `errors` | 方法 → `{"code": n, "message": "…"}`：这些请求以 JSON-RPC 错误回应 |
   | `hang` | 方法名数组：这些请求永不回应 |
   | `diagnostics` | `{"on": ["didOpen","didChange","didSave"], "rules": {"<子串>": [Diagnostic…]}, "delay_ms": n, "version": true}`：文档文本包含某子串时推送对应诊断，否则推送空列表；`version` 为真时带文档版本 |
   | `pull_diagnostics` | 为真时按同一规则应答 `textDocument/diagnostic`（`{"kind":"full","items":[…]}`） |
   | `crash_after_requests` + `state_dir` | 在 `state_dir/count` 中累计请求数（跨重启），达到后 `os.Exit(3)` |
   | `spawn_child` | 启动时起一个 `sleep 600` 子进程，把 `{"child_pid": n}` 写入 `record` |
   | `stderr` | 启动时把该字符串写到 stderr |
   | `echo_env` | 名字数组：应答请求 `fake/env`，返回这些变量的值 |

   程序还必须：`shutdown` 回 `null`，`exit` 后 `os.Exit(0)`；跟踪 `didOpen`/`didChange`（全文）/`didClose` 的文档文本与版本。
2. `buildFakeServer(t)`：照 `pkg/mcp/bridge_test.go:398` 写入临时目录并 `go build`；用 `sync.Once` 保证每个测试二进制只构建一次（把可执行文件放在 `os.MkdirTemp` 目录并在 `TestMain` 结束时删除；若本文件还没有 `TestMain`，在这里定义）。
3. `startFake(t, script map[string]any, mutate func(*InstanceSpec)) (*Instance, string)`：构建 spec（`Command` = 可执行文件路径，`Env` = `os.Environ()` 加 `FAKE_LSP_SCRIPT`，`Root` = `t.TempDir()`，`StartupTimeout` = 5s，`LogPath` = 临时文件），返回实例与 record 文件路径，并 `t.Cleanup(Shutdown)`。
4. `readRecord(t, path) []map[string]any`。

这些辅助函数也会被任务 07、08、09 的测试使用（同包 `_test.go` 可见）。

### 步骤 5：更新 `doc.go`，写测试

`doc.go` 的文件清单加：`instance.go (one server process: environment, initialize, server requests, readiness, restarts), procgroup_*.go (process-tree control and orphan cleanup)`。

测试见「测试计划」。Unix 专属的断言用 `if runtime.GOOS == "windows" { t.Skip(...) }`。

**验证**：`CGO_ENABLED=1 go test -tags fts5 ./pkg/lsp -count=1 -race -timeout 5m` → `ok`。

### 步骤 6：分层与包图

`pkg/lsp` 现在导入 `pkg/home`（Layer 1，不需要登记同层边），fan-out 为 4（`config`、`event`、`home`、`tool`），等于预算上限。

**验证**：`CGO_ENABLED=1 go test -tags fts5 ./pkg/architecture -count=1` → `ok`；`scripts/package-graph.sh && git status --short pkg/architecture/testdata/graph.json` → ` M`（提交它）；`gofmt -l pkg/lsp`、`go vet ./pkg/lsp` 干净。

## 测试计划

全部在 `pkg/lsp/instance_test.go`：

1. `TestBuildEnvPassthroughAndSecrets`：`t.Setenv("GOPATH", "/x")`、`t.Setenv("MY_API_TOKEN", "s")`；`Passthrough: [GOPATH, MY_API_TOKEN]` → 结果含 `GOPATH=/x`，不含 `MY_API_TOKEN`；`Env: {"A": "${GOPATH}/bin", "B": "${NOPE}"}` → `A=/x/bin`，`missing` 含 `NOPE`。
2. `TestBuildEnvProjectScopedReadsOnlyDotEnv`：`FromProject: true`，进程环境有 `FOO=proc`，`<home>/.env` 写 `FOO=file`，`Env: {"X": "${FOO}"}` → `X=file`。
3. `TestExpandCacheDirJSON`：嵌套对象与数组里的占位符都被替换。
4. `TestClientCapabilitiesMatchSpec`：`json.Valid`；`workspace.applyEdit == false`；`general.positionEncodings == ["utf-8","utf-16"]`。
5. `TestStartInstanceHandshake`：脚本 `capabilities: {"positionEncoding": "utf-8", "definitionProvider": true}` → `Encoding()` 为 utf-8，`Capabilities().Supports("definitionProvider")`；record 里 `initialize` 的 `processId` 等于测试进程 pid、`rootUri` 以 `file://` 开头；随后出现 `initialized`。
6. `TestStartInstanceMissingCommand`：命令 `definitely-not-a-server` → 错误含 `not found`。
7. `TestStartInstanceTimeout`：`never_initialize`、`StartupTimeout` 300ms → 1 秒内返回错误；（Unix）进程已消失（`waitForProcessGone` 写法照 `pkg/mcp/bridge_test.go:421`）。
8. `TestServerRequestsAnswered`：`after_initialized` 发 `workspace/configuration`（sections `gopls`、`nope`）、`window/showMessageRequest`（消息 `Import build?`，actions `Import build`/`Not now`，`AutoAnswers: {"Import build": "Import build"}`）、`workspace/applyEdit`、`window/showDocument`、`fake/unknown`；record 中回应分别为设置值与 `null`、`{"title":"Import build"}`、`{"applied":false,...}`、`{"success":false}`、错误码 -32601。
9. `TestPythonPathFilled`：Root 下建 `.venv/bin/python`（空文件、可执行），`workspace/configuration` section `python` → 回应含 `pythonPath` 指向它（Windows 跳过或用 `Scripts\python.exe`）。
10. `TestReadinessProgress`：`Readiness: progress`，`after_initialized` 发 `$/progress` begin（50%）→ 延迟 200ms → end；`quietPeriod` 设 50ms；`WaitReady` 在 end 之后才返回；期间 `Progress()` 为 50。
11. `TestReadinessStatusNotifications`：`rust-analyzer-status` 与 `jdtls-status` 各一个子测试。
12. `TestCrashRestart`：`restartBackoff` 设为 10ms；`crash_after_requests: 1`（`state_dir` 为临时目录）；第一次 `Call` 返回含 `restarted` 的错误；`OnRestart` 被调用；之后状态回到 ready；`MaxRestarts: 1` 时第二次崩溃后 `State() == failed`、`LastError` 含 `crashed`、`Done()` 已关闭。
13. `TestShutdownKillsTree`（Unix）：`spawn_child: true`，`Shutdown` 后服务器与子进程 pid 都消失；`pids.json` 中该记录已删除。
14. `TestSweepOrphans`（Unix）：手工起一个 `sleep 600`（`Setpgid`），把 `{pid, pgid, started: processStarted(pid)}` 写入 pids 文件，`sweepOrphans` 后进程消失、文件为空数组；`started` 不匹配的记录不杀。
15. `TestStderrGoesToLog`：`stderr: "boom"` → 日志文件含 `boom`。
16. `TestAddFolder`：record 中出现 `workspace/didChangeWorkspaceFolders` 且 `added` 为新目录的 URI。
17. `TestRegistrations`：`after_initialized` 发 `client/registerCapability`（method `workspace/didChangeWatchedFiles`，options 含 `watchers`）→ `Registrations("workspace/didChangeWatchedFiles")` 返回该 options；再发 `client/unregisterCapability` 同 id → 返回空。

## 完成判据

- [ ] `ls pkg/lsp/*.go | grep -v _test.go | wc -l` 为 11
- [ ] `CGO_ENABLED=1 go test -tags fts5 ./pkg/lsp -count=1 -race -timeout 5m` 通过，至少 17 个测试
- [ ] `GOOS=windows GOARCH=amd64 go vet ./pkg/lsp` 与 `GOOS=darwin GOARCH=arm64 go vet ./pkg/lsp` 退出码 0
- [ ] `grep -rn "os.Stderr" pkg/lsp/instance.go` 无输出
- [ ] `graph.json` 已重新生成并提交；`CGO_ENABLED=1 go test -tags fts5 ./pkg/architecture -count=1` 通过
- [ ] README 中任务 05 状态为 `DONE`

## STOP 条件

- `pkg/lsp` 的 fan-out 超过 4（说明导入了不该导入的包）。
- 需要给 `pkg/home` 加导出函数。
- 进程树测试在 Linux CI 上两次修复后仍不稳定。
- 假服务器无法用「写源码 + `go build`」的方式构建。

## 维护说明

- `quietPeriod`、`restartBackoff` 是包级变量只为测试可调；生产代码不得修改它们。
- 任务 07 通过 `OnNotification` 接 `publishDiagnostics`，通过 `OnRestart` 重开文档，并读取 `pullStale`；任务 08 负责创建实例、调用 `sweepOrphans`、决定 `LogPath`/`PIDFile` 路径。
- 评审重点：任何路径都不能让服务器写到终端；关闭顺序；崩溃计数窗口；Windows Job Object 句柄释放。
