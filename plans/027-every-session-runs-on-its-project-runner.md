# Plan 027：每个会话跑在自己项目的 runner 上——按项目根自己的信任推导沙箱与审批、gateway 启动目录去语义、去掉"受版本控制"门槛

> **执行者须知**：按步骤顺序执行，每步跑验证命令并确认预期结果后再进下一步。触发
> "STOP conditions" 时停下报告，不要即兴发挥。**不要更新 `plans/README.md`**（评审方维护索引）。
> **禁止提交代码**（owner 手动提交）：不建分支、不 commit、不 push、不 stash；改动留在工作区。
>
> **Drift check（先跑）**：
> - plan 025 与 026 已完成：
>   - `grep -n "func ProjectKeyForRoot" pkg/state/project_store.go` 有 1 行；
>   - `grep -n "func WorkingDir" pkg/tool/state.go` 有 1 行。
>
>   任一没有 → STOP。
> - `grep -n "AppCfg:        baseDeps.AppCfg" pkg/process/runner_pool.go` 有 1 行（项目 runner 沿用基础配置）。
> - `grep -rn "VersionControlled" pkg --include='*.go' | grep -v _test | wc -l` 约 11 行。
>
> 不符 → STOP。

## Status

- **Priority**: P1
- **Effort**: L
- **Risk**: HIGH（改变 gateway 会话的沙箱、审批与可加载的项目配置）
- **Depends on**: 025、026
- **Category**: bug / security / architecture
- **Planned at**: commit `005c1a8` + 主树未提交改动，2026-10-11

## Why this matters

设计文档：`docs/plan/SESSION_BIRTH_DIR_CONTINUATION_DESIGN.md` 2.2、2.3，规则 R1、R4、R6。

1. **已有缺陷**：gateway 的项目 runner 用的是基础 runner 的配置（`runner_pool.go:722` `AppCfg: baseDeps.AppCfg`）。这份配置是按 **gateway 启动目录**的信任推导的沙箱与审批。项目信任与否不起作用，起作用的是 gateway 在哪启动。
2. **R4**：gateway 启动目录没有任何业务语义。今天 gateway 不传启动目录，`process.Open` 就取进程当前目录（`open.go:238-241`），并据此决定：
   - 启动项目；
   - 信任；
   - 项目 MCP / LSP / 技能；
   - 新会话的默认 `cwd`。
3. **R1**：每个会话都跑在它项目的 runner 上。没有项目的会话（025 迁移后只剩主代理级工坊会话）跑在基础 runner 上，工作目录就是 gateway 启动目录。
4. **R6**：项目级 MCP / LSP / 技能 / 项目设置的加载门槛去掉"受版本控制"，只看"受信任 + MCP/LSP 逐条同意"。
5. **项目键按项目根本身**（owner 2026-10-11）：运行时的 `memory.ProjectKey` 也不能再向上找 git 根。

## Current state（已核实）

- **路由**：
  - `Environment.RunnerForSession`（`pkg/process/runner_pool.go:133-147`）→ `RunnerPool.RunnerForSession`（`:484-502`）：会话没有项目、或建条目失败，都返回基础 runner `p.env.Runner`；
  - gateway 的 `runnerFor`（`pkg/gateway/server.go:83-91`）被几十处调用（`grep -rn "runnerFor(" pkg/gateway`）。这些地方都假定拿到非 nil 的 runner，包括读状态、斜杠命令、审批等。
- **项目 runner 的构建** `buildEntryLocked`（`runner_pool.go:659-752`）：
  - `launch := safety.ResolveRegisteredContext(env.Root, project.Root)` 已经按精确根解析；
  - `AppCfg: baseDeps.AppCfg`（`:722`）没有用 `launch`；
  - LSP 的 `VersionControlled`（`:698`）。
- **配置推导**：`safety.EffectiveConfig(cfg, launch)`（`pkg/safety/sandbox_config.go:538-546`）按项目的信任状态补全沙箱与审批，`ResolveEffectiveConfig`（`:589-592`）是"解析 + 推导"的组合。
  - `process.Open` 只保留推导后的配置：`cfgRoot` 拷自 `effective`（`open.go:242-247`），再 `safety.ApplyYOLO(cfgRoot)`；原始配置没有保留。
- **shell 沙箱**：每次调用新建 `safety.NewManager()`，按 `sessionConfig(ctx, st, rt)`（即 runner 的配置）、`req.cwd` 和 `st.AllowedRoots()` 更新（`pkg/tool/shell_tool.go:277-283`）。所以项目 runner 的配置对了，shell 沙箱就对了。
- **gateway 打开进程**：`process.Open(ctx, process.OpenOptions{EnableUploads..., SessionSource: "webchat"})`（`pkg/gateway/serve_run.go:35-43`）没有 `LaunchDir`。
  - `open.go:353-356` 用启动项目设置 `runner.ProjectRoot` / `ProjectKey`；
  - `:363` 用 `launchDir` 设置新会话的默认 `cwd`。
- **"受版本控制"门槛**：
  - `safety.TrustedRoot`（`pkg/safety/runtime.go:321-330`）；
  - `trustedProjectRoot`（`:338-351`）；
  - 项目 MCP：`pkg/process/mcp.go:80`，原因文案 "project is not version controlled"；
  - 技能提议：`pkg/run/skills.go:584`；
  - Codex 导入：`pkg/migrate/codex_assets.go:667`；
  - LSP：`lsp.ManagerOptions.VersionControlled`（`pkg/lsp/manager.go:31`）除了赋值（`open.go:309`、`runner_pool.go:698`）没有任何读取方，是死字段；
  - `safety.Project.VersionControlled`（`pkg/safety/engine.go:935`）由 `Resolve`（`:958-984`）与 `ResolveRegisteredProject`（`sandbox_config.go:507-516`）填写。
- **运行入口**：
  - gateway 的运行在 `server.go:1927` 一带调 `run.RunPipeline(agCtx, s.runnerFor(agCtx, sid), ...)`；
  - cron / 通道 / 心跳走 `pkg/process/run_executor.go:148`（`x.env.RunnerForSession(ctx, sid)`）。
- **项目键**：
  - `memory.ProjectKey`（`pkg/memory/scope.go:299-312`）经 `ProjectRoot` 向上找 `.git`（`:229-253`）；
  - 运行时调用：`pkg/process/open.go:312`、`:355`、`pkg/memory/scope.go:68`；
  - `/migrate` 导入：`pkg/migrate/assets.go`、`codex_assets.go`。

## Commands you will need

| 用途 | 命令 | 成功预期 |
|---|---|---|
| Go | `CGO_ENABLED=1 go test -tags fts5 ./pkg/process/... ./pkg/gateway/... ./pkg/safety/... ./pkg/run/... ./pkg/memory/... ./pkg/migrate/... ./pkg/lsp/... ./pkg/tui/... ./pkg/architecture/... -count=1` | 全部 `ok` |
| 构建 | `CGO_ENABLED=1 go build -tags fts5 ./...` | 无输出 |
| vet / fmt | `CGO_ENABLED=1 go vet -tags fts5 ./pkg/...`；`gofmt -l pkg` | 无输出 |
| 依赖图 | `scripts/package-graph.sh`；`git diff --stat pkg/architecture/testdata/graph.json` | 只多出本计划新增的 import 边（如 `pkg/memory → pkg/state`） |

## Scope

**In scope**：
- `pkg/process/{open.go,runner_pool.go,mcp.go,run_executor.go}`；
- `pkg/gateway/serve_run.go`，以及 gateway 的运行入口（`server.go` 的 start-run 路径）；
- `pkg/safety/{runtime.go,sandbox_config.go}`；
- `pkg/run/skills.go`、`pkg/migrate/codex_assets.go`、`pkg/lsp/manager.go`、`pkg/memory/scope.go`；
- 上述文件的测试；依赖图。

**Out of scope**：
- TUI 启动目录登记、`/resume` 过滤（028）；
- web 选项目界面（029）；
- 技能工坊会话的界面（技能计划）；
- `/migrate` 导入里用 `memory.ProjectRoot` 判断"是否属于当前项目"的匹配逻辑（`migrate/sessions.go:159`、`assets.go:132,156`、`codex_assets.go:73`），保持不变，见 Maintenance notes。

## Git workflow

不建分支、不 commit、不 push、不 stash。

## Steps

### Step 1：保留原始配置，项目 runner 按自己的信任推导

1. `process.Environment` 增加字段 `RawConfig appcfg.Root`，在 `Open` 里存入 `loaded`（推导和 `ApplyYOLO` 之前那一份）。配置热加载（`pkg/process/config_reload.go`）更新配置时同步更新它（`grep -n "AppCfg = \|Deps.AppCfg" pkg/process/config_reload.go` 找写入点）。
2. 新增函数，供 `Open` 与 `buildEntryLocked` 共用：

   ```go
   func effectiveFor(raw appcfg.Root, launch safety.ProjectContext) *appcfg.Root
   ```

   内容是 `EffectiveConfig` + `ApplyYOLO`，与 `open.go:242-247` 现有顺序相同。
3. `buildEntryLocked`：`AppCfg: baseDeps.AppCfg` → `AppCfg: effectiveFor(env.RawConfig, launch)`。
4. `PropagateConfig`（池里的配置下发，`runner_pool.go:790` 起）：给每个条目下发的配置也要按各自的 `launch` 推导，不能把基础 runner 的推导结果直接塞给它们（`grep -n "func (p \*RunnerPool) PropagateConfig" -A40 pkg/process/runner_pool.go`）。

**Verify**：新测试 `TestProjectRunnerDerivesSandboxFromItsOwnTrust`。

- 临时 home；gateway 环境的进程目录是一个**不受信任**的目录 G；
- 项目 P1 已信任、P2 未判断；
- 断言：P1 的 runner `AppCfg.SandboxMode == workspace-write`；P2 是 `read-only`；基础 runner 的推导与 G 无关（见 Step 2）。

### Step 2：gateway 启动目录不再有业务语义（R4）

1. `OpenOptions` 增加 `NoLaunchProject bool`；gateway 的 `serve_run.go:35` 传 `true`。
2. `Open` 里 `NoLaunchProject` 为真时：
   - 不取 `os.Getwd()`；
   - `launchProject` 用零值 `safety.ProjectContext{}`；
   - `effective` 用 `effectiveFor(loaded, safety.ProjectContext{})`；
   - 不设 `runner.ProjectRoot` / `ProjectKey`；
   - `ConfigureMemoryDefaults` 的 `cwd`、`git branch` 传空串；
   - 项目 MCP、LSP、技能都按"没有项目"处理（`ResolveSessionMCP` 收到零值 launch，项目条目自然不加载）。
3. `Environment.LaunchDir` 在这种模式下为空。用 `grep -rn "\.LaunchDir\b\|LaunchProject" pkg --include='*.go' | grep -v _test` 找出 gateway 进程会走到的读取方，逐个确认空值是合法输入；需要非空才能工作的 → STOP 报告。

**Verify**：新测试 `TestGatewayEnvironmentHasNoLaunchProject`：进程目录放一个含 `FOREBRAIN.md`、`.forebrain/mcp_servers.yaml` 的已信任目录，`Open(NoLaunchProject: true)` 之后：
- `env.LaunchProject.Project.Root == ""`；
- 基础 runner 的 `ProjectRoot == ""`；
- MCP 列表不含该目录的项目条目。

### Step 3：没有项目的会话不运行；工坊会话有自己的 runner

1. `RunnerPool.RunnerForSession` 保持签名不变：读状态、斜杠命令等调用方照旧拿基础 runner 做 agent 级读取。
2. 新增 `func (env *Environment) RunnerForTurn(ctx context.Context, sessionID string) (*run.Runner, error)`，**只用于开始一轮**：
   - 会话有项目：
     - 项目根不存在（`os.Stat` 失败）→ 返回 `ErrSessionDirectoryMissing{Dir}`；
     - 否则返回项目条目的 runner。条目建不起来就把错误返回，不再回落基础 runner。
   - 会话没有项目：
     - `source = 'workshop'` → 返回"主代理工坊条目"（下面第 3 点）；
     - 其他 → 返回 `ErrSessionHasNoProject`。
3. **主代理工坊条目**：池里按 agent 建一个特殊条目（键形如 `agent:<id>:workshop`）：
   - `ProjectRoot` = `<主代理工作区>/skills`（目录不存在就创建）；
   - `ProjectKey` 为空；
   - `AppCfg = effectiveFor(raw, ctx)`，这里的 `ctx` 是标记为已信任的 `ProjectContext`（技能目录是 forebrain 自己的目录），按"已信任"推导；
   - 不加载项目 MCP / LSP / 技能；
   - 其余与普通条目相同（同一个 `buildEntryLocked` 分支，传一个合成的 `state.Project{Root: skillsDir}` 和一个标志）。

   若 `buildEntryLocked` 对"没有项目行"的输入有硬依赖（例如按 project ID 记 LRU）→ 改成按键记；做不到 → STOP。
4. 开始一轮的入口都改用 `RunnerForTurn`：
   - gateway 的 start-run 路径（`server.go:1927` 一带，以及它前面构造输入的 `:1751`）；
   - `pkg/process/run_executor.go:148`（cron、通道、心跳）。

   报错时：
   - gateway 发 `run_error`，`code` 分别为 `session_has_no_project`、`session_directory_missing`，`message` 用中英同义的固定文案。缺目录的那条是"这个对话所在的目录已不存在：{dir}。无法继续。"，另一条是"这个对话不属于任何项目，无法运行。"。文案放在 gateway 现有 `run_error` 文案的同一位置（`grep -rn "run_error" pkg/gateway/*.go | grep -v _test | head`）；
   - `run_executor` 返回错误并记日志，任务记为失败。

**Verify**：
- gateway 测试：
  - 绑定到不存在目录的项目的会话，start_run 收到 `run_error`、code 为 `session_directory_missing`，且没有创建 run；
  - 没有项目、`source=''` 的会话收到 `session_has_no_project`；
  - `source='workshop'` 的会话跑在工坊条目上，假工具里 `tool.WorkingDir(ctx)` 等于 `<ws>/skills`。

### Step 4：去掉"受版本控制"门槛（R6）

1. `TrustedRoot`：去掉 `!launch.Project.VersionControlled ||`。
2. `trustedProjectRoot`：去掉 `|| !project.VersionControlled`。
3. `pkg/process/mcp.go:80` 这个 `case` 删掉，后面的"未信任"分支照旧。
4. `pkg/run/skills.go:584`：去掉 `|| !w.project.Project.VersionControlled`，同时改注释（没有项目根才不提议）。
5. `pkg/migrate/codex_assets.go:667`：去掉这段检查与原因文案。
6. `lsp.ManagerOptions.VersionControlled` 是死字段：删掉字段和两处赋值。
7. `safety.Project.VersionControlled` 字段保留（`.git` 保护路径等可能还在用）。用 `grep -rn "\.VersionControlled" pkg --include='*.go' | grep -v _test` 确认剩下的读取方都不是"是否加载项目配置"的判断；如果是 → 一并去掉，并在报告列出。
8. 测试里断言"非 git 项目不加载 MCP / 技能"的用例改为断言"已信任的非 git 项目加载"（`grep -rn "not version controlled\|VersionControlled: false" pkg --include='*_test.go'`）。

**Verify**：新测试：已信任的非 git 目录作为项目时，项目 MCP 条目进入待同意或已加载列表（取决于同意记录），项目技能被加载。

### Step 5：项目键按项目根本身（运行时）

1. `memory.ProjectKey(path)` 改为调用 `state.ProjectKeyForRoot(path)`：不再向上找 git 根，也不再把 worktree 映射到主 worktree。函数注释改写。
2. `pkg/process/open.go:312`、`:355` 改为直接调 `state.ProjectKeyForRoot`（与 025 迁移、项目创建同源）。
3. `memory.ProjectRoot` 保留，只剩 `/migrate` 的匹配逻辑与 TUI 的两处调用（`pkg/tui/render.go:1960`、`chat_session.go:217`，028 处理）。在它的注释里写明：它不是项目身份。
4. 测试：
   - 在仓库子目录调用 `memory.ProjectKey`，返回的是子目录自己的键；
   - 仓库根的键与改动前相同（本机老数据不受影响）。

## Test plan

见各步 Verify。另外跑一遍 gateway 现有的项目会话测试（`grep -rln "RunnerPool\|runnerFor" pkg/gateway/*_test.go pkg/process/*_test.go`）。它们里面依赖"回落基础 runner"的断言要按新语义改，每处改动在报告里说明原因。

## Done criteria

- [ ] Commands 表全部通过
- [ ] `grep -n "AppCfg:        baseDeps.AppCfg" pkg/process/runner_pool.go` 无输出
- [ ] `grep -rn "VersionControlled" pkg --include='*.go' | grep -v _test` 只剩 `safety.Project` 的字段定义、两处填写，以及 Step 4 第 7 点确认过的非加载判断（逐行列在报告里）
- [ ] Step 1–3 的新测试 PASS
- [ ] `git status --short` 中本计划新增的改动只涉及 Scope 列出的文件

## STOP conditions

1. Drift check 不符（025/026 未完成）。
2. gateway 进程里有读取方需要非空的启动目录才能工作。
3. 工坊条目无法复用 `buildEntryLocked`（对项目行有硬依赖），并且改动会超出本计划的文件范围。
4. 去掉"受版本控制"门槛后，发现某处 `.git` 保护逻辑依赖同一个判断，改动会削弱保护。

## Maintenance notes

- 开始一轮一律用 `RunnerForTurn`；`RunnerForSession` 只用来做 agent 级读取。评审时看新增的运行入口用的是哪一个。
- 主代理工坊条目是 R1 的唯一例外，不要再为别的"没有项目的会话"开口子。
- `/migrate` 导入的匹配仍用 `memory.ProjectRoot`（向上找 git 根）判断"这个 Claude/Codex 会话是不是当前项目的"。导入后的项目身份走 `ProjectKey`（精确根）。两者不一致是有意的：匹配宽一些可以把仓库子目录里的历史会话也找出来。owner 想改就另立计划。

---

## 勘误（2026-10-11，执行后由评审方补写）

1. **Step 3「其他 → `ErrSessionHasNoProject`」有一个临时例外**：`RunnerPool.RunnerForTurn` 里保留了
   `if !p.env.noLaunchProject { return p.env.Runner, nil }`（gateway 走 `NoLaunchProject`，仍严格拒绝）。
   原因是 028 之前 TUI 会话还没有项目行，照字面实现会让整套 TUI 特征测试变红；028 已尝试删除，会被
   `pkg/process` 的 9 个 fixture 测试挡住，故保留。**待办**：把 `pkg/process/{worker_cli_test.go,run_executor_test.go}`
   的夹具登记上启动项目后删掉这段回落。
2. **本计划漏改的调用方（已由 038 与一处定点修复补齐）**：去掉启动项目后，`Server.projectKey()` 与
   `Environment.projectScope()` 恒为空，与会话自己的项目 runner 的 `ProjectKey` 分叉；池条目 `buildEntryLocked`
   手搓 runner 也没装 `FileResolver`/`SubagentExecutor`。两者都已在本期修掉，见 043 与 README 的批次说明。
3. `safety.Project.VersionControlled` 在本计划后**已无任何读取方**（只剩字段定义与两处填写），
   Step 4.7 说"确认剩下的读取方不是加载判断"——实际是没有读取方。字段留着但可以后续删除。
