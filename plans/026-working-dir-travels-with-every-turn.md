# Plan 026：工作目录随每一轮走——工具、权限、规则、git 上下文都在会话的项目根执行，不再读进程目录

> **执行者须知**：按步骤顺序执行，每步跑验证命令并确认预期结果后再进下一步。触发
> "STOP conditions" 时停下报告，不要即兴发挥。**不要更新 `plans/README.md`**（评审方维护索引）。
> **禁止提交代码**（owner 手动提交）：不建分支、不 commit、不 push、不 stash；改动留在工作区。
>
> **Drift check（先跑）**：
> - `grep -rn "os\.Getwd()" pkg --include='*.go' | grep -v _test | wc -l`：写计划时为 24 行（列表见 Current state）。
> - `grep -n "func WithProjectRoot\|func ProjectRootFromContext" pkg/tool/state.go`：各 1 行。
> - `grep -n "^func (r \*Runner) RunContent" pkg/run/runner.go`：1 行。
>
> 数量差得多或函数不在 → STOP。

## Status

- **Priority**: P1
- **Effort**: M–L
- **Risk**: MED（改变 gateway 里工具的执行目录；TUI 行为不变）
- **Depends on**: 无（027 依赖本计划）
- **Category**: bug / architecture
- **Planned at**: commit `005c1a8` + 主树未提交改动，2026-10-11

## Why this matters

设计文档：`docs/plan/SESSION_BIRTH_DIR_CONTINUATION_DESIGN.md` 2.1。

全仓库没有 `os.Chdir`。工具执行目录、相对路径、授权判断、FOREBRAIN.md 规则、git 上下文，全都读**进程当前目录**。

- TUI 进程的当前目录就是项目根，结果碰巧正确。
- gateway 一个进程同时跑多个项目的会话，这些读取全部落在 **gateway 启动目录**上。owner 定了 gateway 启动目录没有任何业务语义（R4），所以这是错的。

owner 规则（R1）：工作目录 = 会话所属项目的根。本计划让每一轮都带上自己 runner 的项目根，所有读取都改成先读它。027 再让每个会话跑在自己项目的 runner 上。

## Current state（已核实）

### 上下文里已经有"项目根"

- `tool.WithProjectRoot` / `tool.ProjectRootFromContext`：`pkg/tool/state.go:499-512`。
- 今天只有 subagent 会设置它：`pkg/run/subagent.go:1113`、`:1343`、`:1799`。
- 已经在优先读它的地方：
  - `pkg/run/permissions.go:173`、`:329`；
  - `pkg/tool/request_permissions.go:246`；
  - `pkg/run/runner.go:625`：PermissionRequest 钩子的 `cwd`。主 agent 读不到值时回落到 `r.workspaceRoot()`（`:626-628`），也就是主代理工作区，**这是错的**，钩子协议的 `cwd` 应该是会话目录；
  - `pkg/run/typedsubagent_llm.go:72`：只在 subagent 类型非空时用它拼文件访问范围提示。主 agent 设了它也不影响提示。
- runner 自己的项目根：`Runner.ProjectRoot`（`pkg/run/runner.go:58-64`）。
  - gateway 的项目 runner 设为 `project.Root`（`pkg/process/runner_pool.go:708`）；
  - TUI 在 `pkg/process/open.go:354` 设为启动项目根。
- 每一轮的入口：`Runner.RunContent`（`pkg/run/runner.go:1324` 起），`Run` 也走它（`:1317-1322`）。

### 读进程目录的地方（`grep -rn "os\.Getwd()" pkg cmd --include='*.go' | grep -v _test`）

| 位置 | 用途 | 处理 |
|---|---|---|
| `pkg/tool/shell_tool.go:158` | shell 只读校验用的 cwd | 改读上下文 |
| `pkg/tool/shell_tool.go:955` | `ApplyFilesystemPolicy` 的 cwd 为空时回落 | 调用方传 runner 项目根；回落保留给 TUI |
| `pkg/tool/registry.go:27` | `DefaultAllowedRootsForWorkspace` 把进程目录放在允许根第一位 | 加参数传项目根 |
| `pkg/tool/request_permissions.go:251` | 上下文没有项目根时回落 | 保留回落（上下文从此总有值） |
| `pkg/tool/permissions.go:420`、`:451` | `PathGranted`、`PathDeniedByGrant` 匹配相对授权 | 改为由调用方传 cwd |
| `pkg/run/permissions.go:179`、`:325` | run 层权限判断 | `:325` 先读进程目录再被上下文覆盖，顺序改为先上下文 |
| `pkg/run/runner.go:2196` | MCP 子进程目录 | 改用 `r.ProjectRoot` |
| `pkg/safety/runtime.go:1239` | `NormalizeRequestPermissionsResponse` | 调用方改用 `...AtCWD` 并传上下文里的目录 |
| `pkg/safety/manager.go:308` | 执行命令时 `WorkDir` 为空的回落 | 保证调用方都传 `WorkDir` |
| `pkg/safety/engine.go:962` | `Resolve("")` 读进程目录 | 核对调用方都传了目录 |
| `pkg/safety/sandbox_config.go:652` | `UpdateManagerWithLocalConfig` | 加 cwd 参数 |
| `pkg/assembly/rules_hook.go:121` | FOREBRAIN.md 规则 | 改读请求带的工作目录 |
| `pkg/assembly/sources.go:113` | git 上下文 | 同上 |
| `pkg/turn/executor.go:433` | `/init` | 读 runner 项目根 |
| `pkg/tui/render.go:544`、`:2384`；`pkg/tui/run.go:146`、`:1032`；`pkg/tui/chat_session.go:2380` | TUI 显示、子进程环境 | **不改**：TUI 进程目录 = 项目根 |
| `pkg/process/open.go:240`、`:600`；`cmd/forebrain/*.go` | 进程启动取启动目录 | **不改** |

另外，相对路径用 `filepath.Abs(path)` 解析，同样是读进程目录：`pkg/tool/file_tools.go:1375`（写入路径）。同类写法用 `grep -rn "filepath.Abs(" pkg/tool --include='*.go' | grep -v _test` 找全。

## Commands you will need

| 用途 | 命令 | 成功预期 |
|---|---|---|
| Go | `CGO_ENABLED=1 go test -tags fts5 ./pkg/tool/... ./pkg/run/... ./pkg/safety/... ./pkg/assembly/... ./pkg/turn/... ./pkg/process/... ./pkg/gateway/... ./pkg/tui/... ./pkg/architecture/... -count=1` | 全部 `ok`（`pkg/tui` 的 `TestCharacterizationNetworkApproval` 会访问 example.com，偶发超时，单跑通过即可） |
| 构建 | `CGO_ENABLED=1 go build -tags fts5 ./...` | 无输出 |
| vet / fmt | `CGO_ENABLED=1 go vet -tags fts5 ./pkg/...`；`gofmt -l pkg` | 无输出 |

## Scope

**In scope**：上表标"改"的文件与它们的调用方；`pkg/tool/state.go`（新增读取函数）；各自的测试。

**Out of scope**：
- 每个会话跑在哪个 runner 上、按项目根推导信任（027）；
- TUI 的启动目录处理（028）；
- 表中标"不改"的位置。

## Git workflow

不建分支、不 commit、不 push、不 stash。

## Steps

### Step 1：一个读取函数，一个写入点

1. `pkg/tool/state.go` 在 `ProjectRootFromContext` 旁边新增：

   ```go
   // WorkingDir is the directory a turn's tools act in: the session's project
   // root that the runner put on the context at the turn's entry. A context
   // without one (a test, a command run outside any turn) falls back to the
   // process directory.
   func WorkingDir(ctx context.Context) (string, error)
   ```

   - 上下文里有值：返回它；
   - 否则返回 `os.Getwd()`；
   - 返回值都经过 `filepath.Abs` + `filepath.Clean`。

   再加 `ResolvePath(ctx, p string) (string, error)`：`p` 是绝对路径时直接 `Clean`；否则 `filepath.Join(WorkingDir(ctx), p)`。

2. `Runner.RunContent`（`runner.go:1324`）开头，紧跟 `injectSessionModelSelection` 之后：上下文里还没有项目根、且 `r.ProjectRoot` 非空时，`ctx = tool.WithProjectRoot(ctx, r.ProjectRoot)`。subagent 已设的值不覆盖。

   再查不经过 `RunContent` 的入口：`grep -rn "\.Agent()\.Run\|agent\.Run(\|\.RunContent(\|\.Run(ctx" pkg --include='*.go' | grep -v _test`。若 gateway、cron、通道有绕过 `RunContent` 直接驱动 agent 的路径，在那一处同样注入；找不到注入点 → STOP。

**Verify**：
- 新单测 `TestRunContentPutsProjectRootOnContext`：用一个记录上下文的假工具或假 LLM，`ProjectRoot` 设为临时目录，断言工具收到的 `WorkingDir(ctx)` 等于它；
- 若上下文已有 subagent 设的值，断言保持不变。

### Step 2：工具与授权

1. `shell_tool.go:158`：`os.Getwd()` → `WorkingDir(ctx)`。核对同一函数里真正执行命令传给 `safety.Manager` 的 `WorkDir`（今天由 `:240` → `:465` 的 `req.cwd` 链传递，`grep -n "cwd" pkg/tool/shell_tool.go` 找全），确保来源也是 `WorkingDir(ctx)`。模型传了相对的 `workdir` 参数时，用 `ResolvePath`。
2. 文件工具里所有 `filepath.Abs(<模型给的路径>)` → `ResolvePath(ctx, ...)`。
3. `State.PathGranted`、`State.PathDeniedByGrant`：加 `cwd string` 参数，替换内部的 `os.Getwd()`。调用方（`grep -rn "PathGranted(\|PathDeniedByGrant(" pkg --include='*.go'`）都有上下文，传 `WorkingDir(ctx)`。
4. `DefaultAllowedRootsForWorkspace(workspaceRoot)` → `DefaultAllowedRootsForWorkspace(projectRoot, workspaceRoot)`：`projectRoot` 为空才回落进程目录。调用方 `pkg/run/runner.go:1073` 一带传 `r.ProjectRoot`；`:1089-1091` 的"把项目根前置"变成重复，删掉，免得出现两次。
5. `ApplyFilesystemPolicy`、`safety.UpdateManagerWithLocalConfig`：在 runner 里的调用方传 `r.ProjectRoot`（`grep -rn "ApplyFilesystemPolicy(\|UpdateManagerWithLocalConfig(" pkg --include='*.go'`）。`UpdateManagerWithLocalConfig` 加 `cwd string` 参数；TUI 的调用方传进程目录（等价于今天）。
6. `request_permissions.go:251` 的回落保留不动：上下文从此总有值，这条回落只剩测试会走到。
7. `NormalizeRequestPermissionsResponse` 的调用方改用 `NormalizeRequestPermissionsResponseAtCWD(response, WorkingDir(ctx))`。之后若无人再调用原函数，删掉它。
8. `safety.Manager` 执行命令时 `WorkDir` 为空的回落（`manager.go:308`）：给所有 runner 内的调用方补上 `WorkDir`，回落本身保留（安全网，只服务 runner 之外的调用）。用 `grep -rn "CommandRequest{" pkg --include='*.go' | grep -v _test` 逐个核对。

### Step 3：run 层

1. `pkg/run/permissions.go:325`：先读上下文，没有再 `os.Getwd()`，与 `:173-183` 同一写法。可以直接改用 `tool.WorkingDir(ctx)`。
2. `pkg/run/runner.go:2196` MCP 子进程目录：`os.Getwd()` → `r.ProjectRoot`，为空时回落进程目录。
3. `pkg/run/runner.go:625-628` PermissionRequest 钩子的 `cwd`：回落从 `r.workspaceRoot()` 改为 `tool.WorkingDir(ctx)`。钩子协议的 `cwd` 是会话目录，不是主代理工作区。

### Step 4：上下文组装与 `/init`

1. `pkg/assembly`：
   - 给组装请求（`AssemblyRequest`，`pkg/assembly/types.go`）加 `WorkingDir string` 字段，由发起组装的 runner 代码填 `tool.WorkingDir(ctx)`。发起组装的位置用 `grep -rn "AssemblyRequest{" pkg --include='*.go' | grep -v _test` 找；
   - `GitContextSource.Collect`（`sources.go:113`）改读 `req.WorkingDir`，为空时才回落进程目录；
   - `rules_hook.go` 的 `rulesWorkingDir()`：注释写明"注入和报告读同一处"。把它改成从同一次请求的 `WorkingDir` 取值，注入与上下文报告（`grep -n "rulesWorkingDir" pkg/assembly/*.go`）都改。若报告路径拿不到请求 → STOP。
2. `pkg/turn/executor.go:433` 的 `/init`：改为优先取 turn 上下文里 runner 的项目根（看 `Context` 能拿到什么：`grep -n "func (c Context)" pkg/turn/*.go`），为空才 `os.Getwd()`。

### Step 5：测试

- `pkg/tool`：
  - 工作目录设为临时目录 A、进程目录为 B 时，shell `pwd` 输出 A；
  - `write_file` 写相对路径 `x.txt` 落在 `A/x.txt`；
  - 相对路径的授权按 A 匹配。
- `pkg/assembly`：`WorkingDir` 指向含 FOREBRAIN.md 的临时目录时，规则注入读到它，进程目录里的 FOREBRAIN.md 不被读。
- `pkg/run`：PermissionRequest 钩子收到的 `cwd` 等于项目根，不是主代理工作区。改既有测试的期望前，先确认那条测试断言的就是 workspaceRoot；改的时候在测试名或注释里写明原因。
- gateway：一个 gateway 进程（进程目录为临时目录 G）里两个项目 runner（根 P1、P2），各跑一轮执行 `pwd` 的假工具调用，结果分别是 P1、P2。照现有 gateway 运行测试的样板写（`grep -rn "func TestServer.*Run\|newTestServer" pkg/gateway/*_test.go | head`）。

## Test plan

见 Step 5。

## Done criteria

- [ ] Commands 表全部通过
- [ ] `grep -rn "os\.Getwd()" pkg --include='*.go' | grep -v _test` 只剩：
  - Current state 表里标"不改"的位置；
  - `tool.WorkingDir` 自己的回落；
  - `request_permissions.go` 与 `safety/manager.go` 保留的两处回落；
  - `safety/engine.go` 的 `Resolve("")`（调用方都已传值）。

  逐行列在报告里。
- [ ] `grep -rn "filepath.Abs(" pkg/tool --include='*.go' | grep -v _test` 里没有直接作用于模型给的路径的调用
- [ ] Step 5 的 gateway 双项目测试 PASS
- [ ] `git status --short` 中本计划新增的改动只涉及 Scope 列出的文件

## STOP conditions

1. Drift check 不符。
2. 有驱动 agent 的入口绕过 `RunContent`，而且找不到注入点。
3. 规则报告路径拿不到组装请求，注入和报告无法读同一个目录。
4. 改了 PermissionRequest 钩子的 `cwd` 后，有外部钩子集成测试明确断言 `cwd` 是 workspace（说明有人依赖这个行为）。

## Maintenance notes

- 以后新增需要"当前目录"的代码，一律用 `tool.WorkingDir(ctx)` / `tool.ResolvePath`，不要直接 `os.Getwd()` 或 `filepath.Abs`。评审时 grep 这两个调用。
- 本计划之后，gateway 里工具的执行目录取决于会话跑在哪个 runner 上。027 保证每个会话都跑在自己项目的 runner 上；在 027 完成之前，没有项目的会话仍跑在基础 runner 上，那个 runner 的 `ProjectRoot` 是启动项目根。
