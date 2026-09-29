# 任务 12：`forebrain lsp` 命令、安装、真实服务器集成测试与 CI

> **执行者须知**：逐步执行；每一步运行「验证」命令并确认结果后再进入下一步。出现「STOP 条件」中的任何情况立即停止并汇报。完成后把 `docs/plan/lsp/README.md` 中本任务状态改为 `DONE`。
>
> **规范**：`docs/plan/LSP_CODE_INTELLIGENCE_PLAN.md`。开始前读规范 §1.4、§6.2、§6.4、§6.5、§9.2、§9.3、§11.4、§13、§15 第 4 条、附录 C（`install-unavailable`）。
>
> **前置**：任务 09 已合入（`Manager.Query` 可用；任务 08 的 `DidWrite`、`Snapshot`、`SetEnabled` 可用）。先确认：`grep -n "errNotAvailable" pkg/lsp/control.go` 只剩 `Install`、`DecideRecommendation`、`ResetRecommendations` 三处，否则 STOP。
>
> **漂移检查（先运行）**：`git diff --stat 6305ea9..HEAD -- cmd/forebrain .github/workflows`。与「现状」对比，不符按 STOP 处理。

## 状态

- 优先级：P2 · 工作量：M · 风险：MED（会以用户身份执行安装命令；新增 CI 工作流）
- 依赖：09
- 类别：direction
- 计划基于：`6305ea9`，2026-09-29

## 为什么

用户需要在不打开会话的情况下查看、启用、安装语言服务器，并在出问题时得到能照做的诊断（`doctor`）。推荐框（任务 13）与 `/lsp` 面板（任务 14）的「安装」按钮都调用本任务实现的 `Install`。另外，前面的任务只用假服务器测试；本任务加入对真实服务器的集成测试，证明规范 §1.4 的验收项。

## 现状

- `cmd/forebrain/` 有 8 个生产文件（`gateway.go`、`interactive.go`、`main.go`、`mcp_consent.go`、`resume.go`、`root.go`、`serve.go`、`workspace_trust.go`）；子命令用 cobra，在各自文件的 `init()` 里 `rootCmd.AddCommand(...)`（范例 `cmd/forebrain/resume.go:23`、`gateway.go:11`）。
- `cmd/forebrain/root_test.go:12 TestRootCommandOnlyExposesGatewayAndResume` 断言可见子命令**恰好**是 `gateway`、`resume`：加 `lsp` 命令必须同时改这个测试。
- `rootCmd.PersistentPreRunE = prepPersistentHome`（`root.go:66`）：处理 `--home`，设置 `FOREBRAIN_HOME`。
- 运行时配置：`process.Resolve()`（`pkg/process/runtime.go:54`）返回 `Context{Home, ConfigPath, Config appcfg.Root}`；`process.ActiveAgentWorkspace()`（`pkg/process/open.go:479`）返回当前主 agent 的工作区根。
- 项目与信任：`safety.ResolveProjectContext(home, cwd)`（`pkg/safety/sandbox_config.go:489`）；`safety.TrustedRoot(launch)`（`pkg/safety/runtime.go:302`，未受版本控制或未受信任时返回 `""`）。`cmd/forebrain/mcp_consent.go:27` 是同样用法的范例。
- `pkg/lsp`（任务 03–09 之后）：`ResolveServers`、`LoadEnabled`、`SaveEnabled`、`StateDir`、`CacheDir`、`LogPath`、`Detect`、`UsableInstallRecipe`、`PathFromEnv`、`BuildEnv`、`ExpandCacheDir`、`ExpandCacheDirJSON`、`StartInstance`、`InstanceSpec`、`ResolveRoot`；`Pool.start`（`pool.go`）里内联组装 `InstanceSpec`（任务 08 步骤 1.4）。`pkg/lsp` 有 16 个生产文件（`project.go` 由任务 15 新建），本任务**不新建** `pkg/lsp` 生产文件。
- CI：`.github/workflows/ci.yml` 的 `go` 作业跑全量测试；工作流改动会触发 `lint-workflows.yml`（actionlint）。第三方 action 必须钉到完整 commit SHA（`ci.yml` 里 `pnpm/action-setup` 的写法），`actions/*` 用版本标签。

## 范围

**新建：** `cmd/forebrain/lsp.go`、`cmd/forebrain/lsp_test.go`、`.github/workflows/lsp-integration.yml`。

**修改：** `pkg/lsp/detect.go`、`detect_test.go`、`pool.go`、`control.go`、`control_test.go`、`manager_test.go`（集成测试）、`doc.go`；`cmd/forebrain/root_test.go`；`pkg/architecture/testdata/graph.json`。

**不要碰：** `pkg/tool`、`pkg/run`、`pkg/tui`、`pkg/gateway`、`frontend`、`ci.yml`。`recommendations reset` 子命令由任务 13 加入（它依赖任务 13 定义的推荐状态文件）。

## 步骤

### 步骤 1：抽出实例参数组装（`pool.go`）

把任务 08 在 `Pool.start` 里内联的「环境 → 探测 → 缓存目录 → 占位符展开 → `InstanceSpec`」抽成一个未导出函数，`start` 与 `Doctor` 共用：

```go
// launchPlan is what starting srv at root needs, computed the same way for
// the pool and for forebrain lsp doctor.
type launchPlan struct {
	Spec    InstanceSpec
	Detect  DetectResult
	Missing []string // ${VAR} references that did not resolve
}

func planLaunch(ctx context.Context, srv ServerConfig, root, home, agentWorkspace string) (launchPlan, error)
```

- 行为与任务 08 步骤 1.4 完全相同：`BuildEnv` → `Detect`（未安装时返回与任务 08 相同文本的错误）→ `CacheDir` + `MkdirAll` → `ExpandCacheDir` / `ExpandCacheDirJSON` → 填 `InstanceSpec` 的静态字段（`ServerID`、`Command: det.Path`、`Args`、`Env`、`Root`、`InitializationOptions`、`Settings`、`AutoAnswers`、`Readiness`、`StartupTimeout`、`ShutdownTimeout`、`RestartOnCrash`、`MaxRestarts`、`LogPath`、`PIDFile`）。回调字段（`OnNotification`、`OnStateChange`、`OnRestart`）由调用方填。
- `Pool.start` 改为调用 `planLaunch`，再填回调并 `StartInstance`。

**验证**：`CGO_ENABLED=1 go test -tags fts5 ./pkg/lsp -count=1` → `ok`（纯重构，已有测试全部通过）。

### 步骤 2：`Install`（`detect.go`）

```go
// installTimeout bounds one install command (spec §6.5).
var installTimeout = 10 * time.Minute // tests shorten it

// Install runs the first usable install recipe for srv as the user, on the
// host, with the user's full environment (spec §6.5). progress receives each
// output line (stdout and stderr merged). Only an explicit user action may
// call it: the recommendation dialog, /lsp, or forebrain lsp install.
func Install(ctx context.Context, srv ServerConfig, goos, detectCachePath string, progress func(line string)) error
```

1. `recipe := UsableInstallRecipe(srv, os.Environ(), goos)`；nil → 返回 `fmt.Errorf("no install recipe for %s on this system; install it manually: %s", srv.ID, notes)`，`notes` 为 `srv.Notes` 去首尾空白，为空时用 `"see the server's documentation"`（附录 C `install-unavailable`）。
2. `ctx, cancel := context.WithTimeout(ctx, installTimeout)`；`cmd := exec.CommandContext(ctx, recipe.Argv[0], recipe.Argv[1:]...)`；`cmd.Dir` = `os.UserHomeDir()`（失败则不设）；`cmd.Env = os.Environ()`；stdout 与 stderr 接同一个 `io.Pipe`，`bufio.Scanner`（缓冲上限 1 MiB）逐行调用 `progress`（nil 时丢弃），同时保留最后 20 行。
3. 失败（启动失败、退出码非 0、超时）→ `fmt.Errorf("install command %q failed: %v\n%s", strings.Join(recipe.Argv, " "), err, tail)`，`tail` 为最后 20 行用 `\n` 连接。
4. 成功 → 从 `detectCachePath` 删除所有「文件名与 `filepath.Base(srv.Command)` 相同」的缓存条目（原子写回）；再 `Detect(ctx, srv, env, goos, detectCachePath)`（`env` 用 `BuildEnv` 按服务器规则构建）；仍未安装 → `fmt.Errorf("the install command finished but %s is still not found; add its directory to PATH or set lsp.servers.%s.command to its absolute path", srv.Command, srv.ID)`。

`control.go` 的 `Manager.Install` 替换骨架实现：

- 在 `m.servers()` 里按 id 查找；找不到 → `fmt.Errorf("unknown language server %q", serverID)`。
- 同一服务器已有安装在进行 → `fmt.Errorf("%s is already being installed", serverID)`。
- 安装状态记在 Manager 上（`m.installs map[string]*installState`，`installState{running bool; log []string /* 最后 20 行 */; err string}`），让任何界面都能从快照看到进度，不论安装是谁发起的：开始时 `running = true`、清空 `log` 与 `err`；每行输出追加到 `log`（保留最后 20 行）并转给调用方的 `progress`；结束时 `running = false`，失败则 `err = 错误文本`。每次变化都通知订阅者（沿用任务 08 的 100ms 去抖）。
- `Snapshot()` 把它填进 `LSPServerStatus` 的 `Installing`、`InstallLog`、`InstallError`（任务 02 定义的字段）。
- 调用 `Install(ctx, srv, runtime.GOOS, filepath.Join(StateDir(ws), "detect.json"), progress)`。
- 成功后删除 `m.detected[serverID]`（下一次 `Snapshot` 重新探测）。
- **不**自动启用；启用由调用方（任务 13/14）在安装成功后调用 `SetEnabled`。

**验证**：`CGO_ENABLED=1 go test -tags fts5 ./pkg/lsp -run 'Install' -count=1 -v` → PASS（测试见「测试计划」）。

### 步骤 3：`Doctor`（`detect.go`）

```go
// DoctorInput names what forebrain lsp doctor checks.
type DoctorInput struct {
	Config         *appcfg.Root
	Home           string
	AgentWorkspace string
	ProjectRoot    string // absolute; "" when the directory is not inside a project
	Trusted        bool   // safety.TrustedRoot(launch) != ""
	ServerID       string // "" checks every enabled server
	Handshake      bool   // start each server once and shut it down
}

// DoctorCheck is one line of a server's report.
type DoctorCheck struct {
	Name   string // "config" | "binary" | "environment" | "project" | "handshake"
	Status string // "ok" | "warn" | "fail" | "skip"
	Detail string
}

// DoctorReport is one server's checks, in the order above.
type DoctorReport struct {
	ID          string
	DisplayName string
	Languages   []string
	Enabled     bool
	LogPath     string // the handshake's log, "" when it did not run
	Checks      []DoctorCheck
}

// Doctor checks configured servers; it never enables or installs anything.
func Doctor(ctx context.Context, in DoctorInput) ([]DoctorReport, error)
```

服务器选择：`servers := ResolveServers(ResolveInput{Config, Enabled: LoadEnabled(ws), GOOS: runtime.GOOS})`；`ServerID != ""` 时只取该 id（找不到返回 `unknown language server %q; run forebrain lsp list`）；否则取全部 `Enabled` 的；一个都没有时返回空切片和 nil。

每个服务器依次得到下列检查（前一项 `fail` 时后面的项为 `skip`，Detail 写 `skipped: <前一项名字> failed`）：

| Name | ok | warn | fail |
|---|---|---|---|
| `config` | `Invalid == ""`；Detail：`<scope> entry, fingerprint <Fingerprint>` | — | `Invalid` 非空；Detail = `Invalid` |
| `binary` | `Detect` 已安装；Detail：`<Path>`，有版本时加 ` (<Version>)` | — | 未安装；Detail：`<Command> not found`，有配方时加 `; install with: forebrain lsp install <id>  (runs: <InstallCommand>)`，否则加 `; ` + `Notes` |
| `environment` | `BuildEnv` 的 `missing` 为空；Detail：`passes <k> of <n> allowed variables` + 有值的变量名（逗号分隔，**不输出值**）| `missing` 非空；Detail：`unresolved: ${A}, ${B}` | — |
| `project` | `ProjectRoot != ""` 且 `Trusted`；Detail：`<ProjectRoot>`，项目根存在的根标记加 ` (<marker, …>)`；再附加「提示」（见下） | `ProjectRoot == ""`（Detail：`not inside a project; servers only run in a project`）或未受信任（Detail：`project is not trusted; servers only run in trusted projects`）；或有提示 | — |
| `handshake` | 见下 | — | 启动或初始化失败；Detail：错误文本 |

提示（附加到 `project` 的 Detail，有提示时状态为 `warn`；以 `; ` 连接）：

- `clangd`：项目根及其 `build/` 下都没有 `compile_commands.json`、项目根没有 `compile_flags.txt` → `no compile_commands.json: generate one with cmake -DCMAKE_EXPORT_COMPILE_COMMANDS=ON or bear -- make`。
- `csharp-ls`：项目根有 `*.csproj` 或 `*.sln` 但任何 `obj/project.assets.json` 都不存在（只查项目根与一级子目录）→ `packages are not restored: run dotnet restore`。
- 任何服务器：`ProjectWrites` 非空 → `writes into the project: <a, b>`。

`handshake`：

- `in.Handshake == false` → `skip`，Detail `skipped`。
- 服务器未启用 → `skip`，Detail `skipped: not enabled (forebrain lsp enable <id>)`。
- `ProjectRoot == ""` 或未受信任 → `skip`，Detail `skipped: servers only run in trusted projects`（规范 §9.2：doctor 也不在未受信任目录里执行服务器）。
- 否则：`root` = 项目根（doctor 不针对具体文件，`workspace_folder` 非空时照 `ResolveRoot` 的规则用它）；`plan, err := planLaunch(ctx, srv, root, in.Home, in.AgentWorkspace)`；`start := time.Now()`；`inst, err := StartInstance(ctx, plan.Spec)`；成功后读 `inst.Capabilities()`、`inst.Encoding()`、`inst.PID()`，Unix 上执行 `ps -o rss= -p <pid>`（1 秒超时，失败则省略内存）；然后 `inst.Shutdown(ctx)`。Detail：`initialized in <秒，保留一位小数>s; <encoding>; <能力列表>` + 有内存时 `; <MiB> MiB resident`。能力列表按下列顺序列出支持的项，逗号分隔：`definition`、`references`、`hover`、`document symbols`、`workspace symbols`、`call hierarchy`、`type hierarchy`、`pull diagnostics`（用任务 04 的 `ServerCapabilities` 帮助函数判断）。`LogPath` 填 `plan.Spec.LogPath`。

**验证**：`CGO_ENABLED=1 go test -tags fts5 ./pkg/lsp -run 'Doctor' -count=1 -v` → PASS。

### 步骤 4：`cmd/forebrain/lsp.go`

```go
var lspCmd = &cobra.Command{
	Use:   "lsp",
	Short: "Manage language servers (code intelligence)",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		return fmt.Errorf("forebrain lsp needs a subcommand: list, doctor, enable, disable, install")
	},
}
```

子命令（`init()` 里 `lspCmd.AddCommand(...)`、`rootCmd.AddCommand(lspCmd)`）：

| 子命令 | 参数 / 标志 | 行为 |
|---|---|---|
| `list` | `--project <dir>`（缺省当前目录） | 列出 `ResolveServers` 的全部服务器 |
| `doctor` | `--server <id>`、`--project <dir>`、`--no-handshake` | 调 `lsp.Doctor`；有任何 `fail` 时退出码 1 |
| `enable <id>` | — | `lsp.SaveEnabled(ws, id, true)` |
| `disable <id>` | — | `lsp.SaveEnabled(ws, id, false)` |
| `install <id>` | `--yes` | 确认后调 `lsp.Install` |

共用准备函数：

```go
// lspEnv is what every lsp subcommand needs.
type lspEnv struct {
	home, workspace, projectRoot string
	trusted                      bool
	cfg                          *appcfg.Root
}

func loadLSPEnv(projectDir string) (lspEnv, error)
```

- `rt, err := process.Resolve()`；`ws, err := process.ActiveAgentWorkspace()`；`projectDir` 为空时用 `os.Getwd()`；`launch, err := safety.ResolveProjectContext(rt.Home, projectDir)`，出错时 `projectRoot = ""`、`trusted = false`（不报错，与 `mcp_consent.go` 一致地 fail closed）；否则 `projectRoot = launch.Project.Root`、`trusted = safety.TrustedRoot(launch) != ""`。
- `enable`/`disable`/`install` 先用 `ResolveServers` 确认 id 存在，否则返回 `unknown language server "<id>"; run forebrain lsp list`。

输出格式（写到 `cmd.OutOrStdout()`，用 `text/tabwriter`，`minwidth 0, tabwidth 8, padding 2`）：

`list`：

```
ID                          LANGUAGES             ENABLED  BINARY
gopls                       Go                    yes      /home/u/go/bin/gopls (v0.20.0)
pyright                     Python                no       not installed · forebrain lsp install pyright
rust-analyzer               Rust                  no       not installed · no install recipe on this system
```

- 排序：已启用在前，然后按 id。`LANGUAGES` 为 `Languages` 用 `, ` 连接（自定义条目为扩展名列表）；`Invalid` 非空的行 `BINARY` 列写 `invalid: <Invalid>`。
- 探测用 `lsp.Detect`（并发，最多 4 个同时进行；`detect.json` 缓存，所以第二次很快）。
- 末尾空一行后按情况输出：`projectRoot == ""` → `Not inside a project: language servers start only in trusted projects.`；未受信任 → `This project is not trusted: language servers do not start here.`

`enable <id>` 成功输出（两行）：

```
Enabled <id>. Edits in trusted projects get its diagnostics from the next edit on.
The lsp tool appears in sessions started from now on.
```

`disable <id>`：`Disabled <id>. Running sessions stop using it from their next edit; instances already running exit when idle.`（依据：任务 08 的 `servers()` 在 `enabled.json` 的 mtime 变化时重建，所以别的进程里的 Manager 下一次访问就不再选它；已运行的实例由 janitor 在 `idle_timeout` 后关闭。）

`doctor` 输出：每个服务器一段，段间空一行：

```
gopls (Go) · enabled
  ok    config       catalog entry, fingerprint 3f2a9c0d1e4b
  ok    binary       /home/u/go/bin/gopls (golang.org/x/tools/gopls v0.20.0)
  ok    environment  passes 2 of 17 allowed variables: GOPATH, HTTPS_PROXY
  ok    project      /work/app (go.mod)
  ok    handshake    initialized in 0.8s; utf-16; definition, references, hover, document symbols, workspace symbols, call hierarchy, type hierarchy; 142 MiB resident
        log          /home/u/.forebrain/workspace/state/lsp/logs/gopls-1a2b3c4d.log
```

没有已启用服务器且未给 `--server` → 输出 `No language server is enabled. Run forebrain lsp list to see the available ones.`，退出码 0。

`install <id>`：

1. 计算 `recipe := lsp.UsableInstallRecipe(srv, os.Environ(), runtime.GOOS)`；nil → 返回附录 C 的 `install-unavailable` 文本（直接调用 `lsp.Install` 也会得到它）。
2. 打印 `This runs on your machine, outside the sandbox:` 与下一行 `  <argv 用空格连接>`。
3. 没有 `--yes`：stdin 是终端时问 `Run it? [y/N] `，回答不是 `y`/`yes` 时输出 `Cancelled.` 并返回 nil；stdin 不是终端时返回错误 `pass --yes to install without a prompt`。
4. `lsp.Install(ctx, srv, runtime.GOOS, filepath.Join(lsp.StateDir(ws), "detect.json"), func(line string) { fmt.Fprintln(out, "  " + line) })`。
5. 成功输出 `Installed <id>.`；未启用时再输出 `Enable it with: forebrain lsp enable <id>`。

**验证**：`CGO_ENABLED=1 go build -tags fts5 ./cmd/forebrain` → 退出码 0；`CGO_ENABLED=1 go run -tags fts5 ./cmd/forebrain --home "$(mktemp -d)" lsp list` 输出包含 `gopls` 与 `pyright` 两行，退出码 0。

### 步骤 5：更新 `root_test.go`

`TestRootCommandOnlyExposesGatewayAndResume` 改名为 `TestRootCommandExposesItsSubcommands`，期望列表改为 `[]string{"gateway", "lsp", "resume"}`；其余断言不变。

### 步骤 6：真实服务器集成测试（`pkg/lsp/manager_test.go`）

**为什么不用 build tag**：`TestTestFilesCorrespondToProductionFiles` 要求 `X_test.go` 对应 `X.go`，所以不能新建 `integration_test.go`；也不能把 build tag 加到已有测试文件上（会让该文件的单元测试在默认构建里消失）。因此集成测试写在 `manager_test.go`，用环境变量开关。

```go
// Real-server integration tests (spec §1.4). They run only when
// FOREBRAIN_LSP_INTEGRATION lists the servers to exercise ("all" for every
// case), and skip a case whose binary is missing unless
// FOREBRAIN_LSP_INTEGRATION_REQUIRE=1.
func TestIntegrationRealServers(t *testing.T)
```

- 未设置 `FOREBRAIN_LSP_INTEGRATION` → `t.Skip("set FOREBRAIN_LSP_INTEGRATION to run real language servers")`。
- 用例表 `integrationCases []integrationCase`，每个用例：

  ```go
  type integrationCase struct {
  	server   string            // catalog id
  	files    map[string]string // fixture project, written under t.TempDir()
  	file     string            // relative path used for the queries
  	line     int               // 1-based line holding symbol
  	symbol   string            // a call of a function defined in defFile
  	defFile  string            // where definition must point
  	broken   string            // content of file that introduces one error
  	settle   time.Duration     // extra wait for slow servers (rust-analyzer's cargo check)
  }
  ```

  fixture 全部以字符串常量写在测试文件里（**不**放 `testdata`，理由见规范 §3.14）。首批用例：

  | server | 项目文件 | 查询位置 | 引入的错误 |
  |---|---|---|---|
  | `gopls` | `go.mod`（`module example.com/fixture` + `go 1.22`）、`calc.go`（`func Add(a, b int) int { return a + b }`）、`main.go`（`func main() { _ = Add(1, 2) }`） | `main.go` 调用 `Add` 的那一行，symbol `Add` | `_ = Add(1, "x")` |
  | `pyright` | `pyproject.toml`（`[project]\nname = "fixture"\n`）、`calc.py`（`def add(a: int, b: int) -> int:`）、`main.py`（`from calc import add` + `print(add(1, 2))`） | `main.py` 调用行，symbol `add` | `print(add(1))` |
  | `typescript-language-server` | `tsconfig.json`（`{"compilerOptions":{"strict":true}}`）、`calc.ts`（`export function add(a: number, b: number): number`）、`main.ts`（`import { add } from "./calc";` + `console.log(add(1, 2));`） | `main.ts` 调用行，symbol `add` | `console.log(add(1, "x"));` |
  | `rust-analyzer` | `Cargo.toml`（`[package] name = "fixture" version = "0.1.0" edition = "2021"`）、`src/calc.rs`（`pub fn add(a: i32, b: i32) -> i32`）、`src/main.rs`（`mod calc;` + `fn main() { println!("{}", calc::add(1, 2)); }`） | `src/main.rs` 调用行，symbol `add` | `println!("{}", calc::add(1, "x"));`（`settle` 90s） |
  | `clangd` | `compile_flags.txt`（`-std=c11`）、`calc.h`、`calc.c`、`main.c`（`#include "calc.h"` + `int main(void) { return add(1, 2); }`） | `main.c` 调用行，symbol `add` | `return add(1, undefined_name);` |
  | `clangd`（C++） | `compile_flags.txt`（`-std=c++17`）、`calc.hpp`、`calc.cpp`、`main.cpp` | 同上 | 同上 |
  | `jdtls`、`kotlin-lsp`、`sourcekit-lsp`、`metals`、`csharp-ls`、`intelephense` | 各自最小工程（`pom.xml` / `settings.gradle.kts` + `build.gradle.kts` / `Package.swift` / `build.sbt` / `*.csproj` / `composer.json`），同样「一个定义文件 + 一个调用文件」 | 调用行 | 参数类型错误或未定义名字 |

  最后一行的六个用例由执行者按同一模式写出；每个工程必须是该语言工具链能直接识别的最小形式，写完后在本机（或 CI 的 extended 作业）实际跑通一次再提交。

- 每个用例的步骤（子测试 `t.Run(case.server+"/"+ext, …)`）：
  1. 服务器未安装：`FOREBRAIN_LSP_INTEGRATION_REQUIRE=1` 时 `t.Fatalf`，否则 `t.Skipf`。
  2. 写 fixture；配置 `cfg := &appcfg.Root{}`，`cfg.LSP.Servers = map[string]appcfg.LSPServerConfig{server: {Enabled: appcfg.BoolPtr(true)}}`、`wait := 30000; cfg.LSP.Diagnostics.WaitMS = &wait`、`cfg.LSP.RequestTimeout = 300`；`NewPool(cfg)`、`NewManager(ManagerOptions{Home, AgentWorkspace: t.TempDir(), ProjectRoot: dir, Trusted: true, VersionControlled: true})`；`t.Cleanup` 里关闭。
  3. `Query(definition, file, line, symbol)` → 结果文本包含 `defFile`；`references`（`IncludeDeclaration: true`）→ 至少 2 个结果；`hover` → 不以 `no hover information` 开头；`document_symbols`（对 `defFile`）→ 包含定义的函数名。
  4. `DidWrite(ctx, "it-session", []FileChange{{file, 原内容, broken}})`（先把 `broken` 写盘）→ `Summary.New >= 1`，或 `PendingFiles` 包含该文件；pending 时轮询 `Query(diagnostics, file)` 直到出现 `error`（上限 `settle` + 60 秒）。
  5. 写回原内容，`DidWrite(… {file, broken, 原内容})` → `Summary.New == 0`；随后轮询 `Query(diagnostics, file)` 直到首行为 `diagnostics for <file>: 0`（上限同上）。
- `FOREBRAIN_LSP_INTEGRATION` 的取值：`all`，或逗号分隔的 server id（只跑列出的）。

**验证**：本机至少装有 `gopls` 时：`FOREBRAIN_LSP_INTEGRATION=gopls FOREBRAIN_LSP_INTEGRATION_REQUIRE=1 CGO_ENABLED=1 go test -tags fts5 ./pkg/lsp -run TestIntegrationRealServers -count=1 -v -timeout 20m` → PASS。不设置变量时同一命令输出 `SKIP`。

### 步骤 7：CI 工作流 `.github/workflows/lsp-integration.yml`

新建独立工作流（不改 `ci.yml`，不成为必需检查）：

```yaml
name: LSP integration
on:
  pull_request:
    branches: [main]
    paths: ['pkg/lsp/**', '.github/workflows/lsp-integration.yml']
  schedule:
    - cron: '17 3 * * *'
  workflow_dispatch:

permissions:
  contents: read

jobs:
  core:
    name: Core language servers (non-blocking)
    runs-on: ubuntu-latest
    timeout-minutes: 45
    continue-on-error: true
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with:
          go-version-file: go.mod
          cache-dependency-path: go.sum
      - uses: actions/setup-node@v4
        with:
          node-version: "22"
      - name: Install language servers
        run: |
          sudo apt-get update && sudo apt-get install -y build-essential clangd
          go install golang.org/x/tools/gopls@latest
          npm install -g pyright typescript-language-server typescript
          rustup component add rust-analyzer
          echo "$(go env GOPATH)/bin" >> "$GITHUB_PATH"
      - name: Integration tests
        env:
          CGO_ENABLED: "1"
          FOREBRAIN_LSP_INTEGRATION: gopls,pyright,typescript-language-server,rust-analyzer,clangd
          FOREBRAIN_LSP_INTEGRATION_REQUIRE: "1"
        run: go test -tags fts5 ./pkg/lsp -run TestIntegrationRealServers -count=1 -v -timeout 30m

  # extended-linux and extended-macos follow; see the bullets below.
```

- 另加两个作业，都带 `if: github.event_name != 'pull_request'`（只在 nightly 与手动触发时运行）：`extended-linux`（`ubuntu-latest`；`actions/setup-java@v4` 装 Temurin 21，`actions/setup-dotnet@v4` 装 8.0，`actions/setup-node@v4`；`npm install -g intelephense`；`dotnet tool install --global csharp-ls`；jdtls、kotlin-lsp、metals 按各自官方文档的发行包安装——**把下载地址与版本号写在作业的 `env` 里并钉死版本**，不用 `latest` 链接）与 `extended-macos`（`macos-14`，只跑 `sourcekit-lsp`）。两者都 `continue-on-error: true`，`FOREBRAIN_LSP_INTEGRATION` 列出各自的 server id，`FOREBRAIN_LSP_INTEGRATION_REQUIRE: "1"`。
- **只用 `actions/*` 官方 action**；任何第三方 action 必须钉到完整 commit SHA 并带版本注释（照 `ci.yml` 的 `pnpm/action-setup`）。

**验证**：`go run github.com/rhysd/actionlint/cmd/actionlint@v1.7.12` → 退出码 0、无输出。

### 步骤 8：`doc.go` 与全量检查

`doc.go` 中 `detect.go` 的描述改为 `detect.go (finding, versioning and installing server binaries; forebrain lsp doctor)`。

**验证**：`gofmt -l cmd pkg third_party` 无输出；`go vet ./...` 退出码 0；`scripts/package-graph.sh` 后提交 `graph.json`；`CGO_ENABLED=1 go test -tags fts5 ./... -count=1 -timeout 20m` → 全部 `ok`；`ls pkg/lsp/*.go | grep -v _test.go | wc -l` 为 16（任务 15 之前）。

## 测试计划

- `pkg/lsp/detect_test.go`
  - `TestInstallRunsRecipeAndStreamsOutput`：在临时目录写一个可执行脚本 `fake-installer`（`#!/bin/sh` 输出两行后在 `$TARGET_DIR` 创建 `fakels` 可执行文件）并放进 PATH（`t.Setenv("PATH", …)`）；服务器配置 `Command: "fakels"`、`Install: [{Requires: "fake-installer", Argv: ["fake-installer"]}]` → `progress` 依次收到两行；返回 nil；`detect.json` 中旧的 `fakels` 条目被删除。Windows 上 `t.Skip`。
  - `TestInstallUnavailable`：`Install` 为空 → 错误文本逐字等于 `no install recipe for x on this system; install it manually: see the server's documentation`。
  - `TestInstallFailureIncludesTail`：脚本输出 30 行后 `exit 3` → 错误含 `failed:` 与最后 20 行、不含第 10 行。
  - `TestInstallStillMissing`：脚本成功但不创建二进制 → 错误含 `is still not found`。
  - `TestInstallTimeout`：`installTimeout = 200 * time.Millisecond`，脚本 `sleep 5` → 1 秒内返回错误。
  - `TestDoctorStaticChecks`：`Handshake: false`；三个服务器：未安装的目录服务器（`binary` fail，`environment`/`project`/`handshake` skip）、`Invalid` 的自定义条目（`config` fail）、已装的假服务器（全部 ok 或 warn，`handshake` skip）。
  - `TestDoctorHints`：clangd 条目 + 无 `compile_commands.json` 的项目 → `project` 为 warn 且 Detail 含 `cmake -DCMAKE_EXPORT_COMPILE_COMMANDS=ON`。
  - `TestDoctorHandshake`：用任务 05 的 `buildFakeServer` 构建假服务器（`capabilities` 含 definition 与 references）→ `handshake` ok，Detail 以 `initialized in ` 开头并含 `definition, references`；`Shutdown` 后进程不存在（`waitForProcessGone` 同款轮询）。
  - `TestDoctorSkipsUntrusted`：`Trusted: false` → `handshake` skip，Detail 为 `skipped: servers only run in trusted projects`，且假服务器从未启动（脚本的 `record` 文件不存在）。
- `pkg/lsp/control_test.go`
  - `TestManagerInstallUnknownServer`；`TestManagerInstallNotifiesSubscribers`（用上面的假安装器；安装进行中 `Snapshot` 该服务器 `Installing == true` 且 `InstallLog` 有输出行，结束后 `Installing == false`）；`TestManagerInstallFailureInSnapshot`（失败后 `InstallError` 非空）；`TestManagerInstallRejectsConcurrent`（第二次调用返回 `is already being installed`）。
  - 删除骨架测试里对 `Install` 返回 `errNotAvailable` 的断言。
- `pkg/lsp/manager_test.go`：`TestIntegrationRealServers`（步骤 6）。
- `cmd/forebrain/lsp_test.go`（`t.Setenv("FOREBRAIN_HOME", t.TempDir())`、`process.ResetResolve()`，通过 `rootCmd.SetArgs([...])` + `rootCmd.SetOut(&buf)` + `rootCmd.Execute()` 调用；每个测试结束恢复 `SetArgs(nil)` 与输出）：
  - `TestLSPListShowsCatalog`：输出含表头 `ID`、`gopls` 与 `pyright` 行，末尾含 `Not inside a project` 或 `not trusted` 提示之一。
  - `TestLSPEnableDisable`：`lsp enable gopls` → `enabled.json` 中 `gopls` 为 true，输出第一行以 `Enabled gopls.` 开头；`lsp disable gopls` → false。
  - `TestLSPEnableUnknown`：错误文本为 `unknown language server "nope"; run forebrain lsp list`。
  - `TestLSPInstallNeedsYesWithoutTerminal`：stdin 非终端、无 `--yes` → 错误 `pass --yes to install without a prompt`，安装器未运行。
  - `TestLSPDoctorNothingEnabled`：输出 `No language server is enabled. …`，退出码 0（`Execute` 返回 nil）。
- `cmd/forebrain/root_test.go`：`TestRootCommandExposesItsSubcommands`。

## 完成判据

- [ ] 上述测试全部通过；`TestIntegrationRealServers` 在未设变量时 SKIP，在本机设 `gopls` 时 PASS（在 PR 描述里贴出实际命令与结果；本机没有任何真实服务器时写明「未能本机运行」）
- [ ] `grep -n "errNotAvailable" pkg/lsp/control.go` 只剩 `DecideRecommendation`、`ResetRecommendations` 两处
- [ ] `forebrain lsp --help` 列出 `list`、`doctor`、`enable`、`disable`、`install`
- [ ] actionlint 通过；新工作流只使用 `actions/*` 或已钉 SHA 的 action
- [ ] `graph.json` 已提交；全量测试通过
- [ ] README 中任务 12 状态为 `DONE`

## STOP 条件

- 需要在模型可调用的路径（`tool.CodeIntelligence` 或任何工具）上触发安装。
- 需要通过 shell（`sh -c`）执行安装配方。
- 需要让 `doctor` 在未受信任或未启用的情况下启动服务器。
- 某个语言服务器的安装需要未钉 SHA 的第三方 action，或需要仓库密钥。
- `pkg/lsp` 生产文件数需要超过 16（任务 15 之前）。

## 维护说明

- 目录新增服务器（任务 17）时：若它需要 doctor 提示，加进步骤 3 的提示表；若要进 CI，加一个集成用例并把 id 加进相应作业的 `FOREBRAIN_LSP_INTEGRATION`。
- 集成作业是 `continue-on-error`，失败不会拦 PR；每周看一次 nightly 结果，连续失败要么修复、要么在目录 `notes` 里说明该服务器的已知问题。
- 评审重点：`Install` 的环境是完整 `os.Environ()`（与服务器进程的受限环境不同，是有意为之：安装命令需要用户的代理、包管理器配置）；安装命令在执行前完整展示。
