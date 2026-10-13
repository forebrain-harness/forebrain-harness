# Plan 028：TUI——启动目录就是项目（登记进项目表）、数据目录内拒绝启动、`/resume` 只列本项目、跨项目 `resume` 拒绝、粘贴截图存进项目

> **执行者须知**：按步骤顺序执行，每步跑验证命令并确认预期结果后再进下一步。触发
> "STOP conditions" 时停下报告，不要即兴发挥。**不要更新 `plans/README.md`**（评审方维护索引）。
> **禁止提交代码**（owner 手动提交）：不建分支、不 commit、不 push、不 stash；改动留在工作区。
>
> **Drift check（先跑）**：
> - 025、026、027 已完成：`grep -n "func (s \*ProjectStore) FindOrCreateByRoot" pkg/state/project_store.go`、`grep -n "func (env \*Environment) RunnerForTurn" pkg/process/runner_pool.go`，各 1 行。
> - `grep -n "func workspaceTrustTarget" -A3 cmd/forebrain/workspace_trust.go`：用的是 `safety.Resolve(cwd)`。
> - `grep -n "memory.ProjectRoot(cwd)" pkg/tui/chat_session.go`：1 行。
>
> 不符 → STOP。

## Status

- **Priority**: P1
- **Effort**: M
- **Risk**: MED（TUI 在仓库子目录启动时的信任、计划目录、项目记忆都会改为以该子目录为准）
- **Depends on**: 025、026、027
- **Category**: feature / semantics
- **Planned at**: commit `005c1a8` + 主树未提交改动，2026-10-11

## Why this matters

设计文档：`docs/plan/SESSION_BIRTH_DIR_CONTINUATION_DESIGN.md` 2.4，规则 R2、R5、R8。owner 2026-10-11 的原话：

> 改一下需求，git仓库子目录下启动tui进程，不再向上找git根，在哪个目录下启动tui，哪个目录就是项目……不自作主张替用户找git根，也许用户的需求就是想把这个子目录作为一个新项目

- TUI 的项目必须进项目表，web 端有对应的项目空间。
- FOREBRAIN_HOME 内（含子目录）不能启动 TUI。
- `/resume` 只列当前项目的会话；`resume <id>` 指向别的项目 → 拒绝并提示到那个目录运行。
- TUI 粘贴的截图存进 `<项目根>/attachments/`，不再放系统临时目录。

## Current state（已核实）

- **TUI 入口** `runStreamingTerminalWithInitialSessionID`（`cmd/forebrain/interactive.go:55-115`）：
  - 先问信任（`interactiveEnsureWorkspaceTrusted(..., root, "")`，`:68`）；
  - 再问项目 MCP 与 LSP 的同意（`:77-85`）；
  - 然后 `cwd, err := os.Getwd()`（`:107`），`interactiveOpenChatSession(ctx, cwd)`（`:112`）。
- **信任目标**：`workspaceTrustTarget`（`cmd/forebrain/workspace_trust.go:85-91`）用 `safety.Resolve(cwd)`，**向上找 `.git`**，所以在仓库子目录启动时信任的是仓库根。MCP、LSP 同意的目标同理（`grep -n "safety.Resolve\|ResolveProjectContext" cmd/forebrain/*.go` 找全）。
- **打开会话**：`openProcessChatSession` → `process.Open(OpenOptions{LaunchDir: cwd})`（`pkg/tui/notify.go:1290-1292`）。`Open` 里 `ResolveEffectiveConfig(loaded, root, launchDir)` → `ResolveProjectContext`（`pkg/safety/sandbox_config.go:489`），向上找 `.git`。
- **系统提示里的项目根**：`projectContextPreHook`（`pkg/tui/chat_session.go:208-235`）每轮注入 `project_root: memory.ProjectRoot(cwd)`（`:217`），是 git 根。`pkg/tui/render.go:1960` 显示技能卡时同样用 `memory.ProjectRoot`。
- **会话出生**：
  - `SessionStore.ensureSession`（`pkg/state/session_store.go:183-225`）插入时**不写 `project_id`**，项目绑定是另一次调用（`ProjectStore.BindSession`）；
  - 默认目录来自 `ConfigureMemoryDefaults`（`:104-114`）；
  - 子会话的 `parent_session_id` 来自 `SessionBirth`。
- **`/resume`**：
  - `handleResume`（`pkg/tui/commands.go:50-62`）→ `promptRecentSession`（`pkg/tui/run.go:4757` 起）→ `ChatSession.ListSessionsRecentPaged` / `ListSessionsRecent`（`pkg/tui/chat_surface.go:238-262`），列出该主代理全部会话；
  - 按项目列的接口已经存在：`ListSessionsForProject`（`pkg/state/session_store.go:768` 起）。
- **`resume <id>`**：`cmd/forebrain/resume.go:11-19`，直接打开该会话。
- **项目根校验**：gateway 的 `validateProjectRoot`（`pkg/gateway/api_extra.go:4855-4891`）检查：
  - 目录存在、是目录；
  - 规范化；
  - 不是文件系统根；
  - 不是 FOREBRAIN_HOME，也不在它里面。
- **粘贴截图**：`systemClipboardImageReader.ReadClipboardImage`（`pkg/tui/tty_session.go:457` 起）写进 `os.TempDir()`。

## Commands you will need

| 用途 | 命令 | 成功预期 |
|---|---|---|
| Go | `CGO_ENABLED=1 go test -tags fts5 ./cmd/forebrain/... ./pkg/tui/... ./pkg/state/... ./pkg/process/... ./pkg/safety/... ./pkg/gateway/... ./pkg/architecture/... -count=1` | 全部 `ok`（`TestCharacterizationNetworkApproval` 偶发联网超时，单跑通过即可） |
| 构建 | `CGO_ENABLED=1 go build -tags fts5 ./...` | 无输出 |
| vet / fmt | `CGO_ENABLED=1 go vet -tags fts5 ./cmd/... ./pkg/...`；`gofmt -l pkg cmd` | 无输出 |

## Scope

**In scope**：
- `cmd/forebrain/{interactive.go,resume.go,workspace_trust.go}` 及 MCP/LSP 同意的目标解析；
- `pkg/tui/{notify.go,chat_session.go,chat_surface.go,commands.go,run.go,render.go,tty_session.go}`；
- `pkg/state/session_store.go`（出生时带项目）；
- 项目根校验函数下沉（从 `pkg/gateway/api_extra.go` 移到 `pkg/state`）；
- `pkg/process/open.go`（启动项目改用精确根）；
- 上述文件的测试。

**Out of scope**：
- web 端选项目（029）；
- web 附件"保存到"（030）；
- 回到这里 `/rewind`（033）；
- `/migrate` 导入的匹配逻辑（`memory.ProjectRoot` 在 `pkg/migrate` 的用法不动）。

## Git workflow

不建分支、不 commit、不 push、不 stash。

## Steps

### Step 1：项目根校验共用一个函数

1. 把 `validateProjectRoot` 移到 `pkg/state`，改名 `ValidateProjectRoot(home, candidate string) (string, error)`，逻辑不变。gateway 改为调用它（原函数删掉）。错误文案保持英文原样（gateway 测试可能断言），TUI 另配中文提示。
2. 用 `grep -rn "validateProjectRoot" pkg --include='*.go'` 确认所有调用方都改过来。

### Step 2：会话出生就带项目

1. `SessionBirth` 增加 `ProjectID string`。`SessionStore` 的默认值增加 `projectID`：新方法 `ConfigureProjectDefault(projectID string)`，与 `ConfigureMemoryDefaults` 同一把锁。
2. `ensureSession` 插入时写 `project_id`，按以下顺序取值：
   1. 有 `ParentSessionID` → 取父会话的 `project_id`（`(SELECT project_id FROM fb_sessions WHERE id=?)`）；
   2. 否则取 `birth.ProjectID`；
   3. 再否则取默认值；
   4. 都为空 → `NULL`。

   冲突更新分支不改 `project_id`（出生即固定）。
3. 子会话的 `cwd` 照旧来自 birth / 默认值。父会话有项目时，子会话 `cwd` 为空就用父会话的 `cwd`，在同一条 SQL 里取。

**Verify**：`pkg/state` 新测试：
- 设了默认项目的 store 新建会话，`project_id` 正确；
- 子会话继承父会话的项目与目录；
- 冲突更新不改 `project_id`。

### Step 3：启动目录登记为项目（R2）

在 `runStreamingTerminalWithInitialSessionID` 里，**信任提示之前**：

1. `cwd` 改为在函数开头取（`os.Getwd()` 提前），然后 `root, err := state.ValidateProjectRoot(home, cwd)`。
   - 失败 → 往 stderr 打印中文提示，以非 0 退出，不进入 TUI：
     - FOREBRAIN_HOME 内或其子目录：`不能在 forebrain 的数据目录里启动，请到项目目录下运行。`
     - 文件系统根：`不能在文件系统根目录启动，请到项目目录下运行。`
     - 其他：原错误。
2. 信任提示、MCP 同意、LSP 同意的目标都改为这个精确根，不再 `safety.Resolve` 向上找：
   - `workspaceTrustTarget` 改用 `safety.ResolveRegisteredProject(root)`（`sandbox_config.go:507`）；
   - MCP / LSP 同意解析项目的地方同样改（`grep -n "safety.Resolve(\|ResolveProjectContext(" cmd/forebrain/*.go pkg/tui/*.go`）。
3. `process.Open` 的启动项目改用精确根。给 `OpenOptions` 增加 `ExactLaunchRoot bool`，TUI 传 `true`；`Open` 里此时用 `safety.ResolveRegisteredContext(root, launchDir)` 代替 `ResolveProjectContext`。gateway 走 027 的 `NoLaunchProject`，不受影响。
4. 打开会话后，TUI 用当前主代理的 `ProjectStore.FindOrCreateByRoot(ctx, root, 默认值)` 找或建项目：
   - 名字取目录名，其余字段与 web 创建项目的默认值相同；
   - 把得到的项目 ID 交给 `sessStore.ConfigureProjectDefault(id)`，存在 `ChatSession` 上（新字段 `ProjectID`）；
   - 切换主代理会换一套 store，切换后在新主代理下重新找或建（`grep -n "func.*[Ss]witch.*Agent\|agent_switch" pkg/tui/*.go pkg/process/agent_switch.go`）。
5. `ChatSession` 打开时若带了已有会话 ID（resume），不在这里创建，见 Step 5。

**Verify**：
- `cmd/forebrain` 测试：
  - 在临时 home 的 `workspace` 子目录里启动 → 非 0 退出，输出含"数据目录"；
  - 在临时目录 `repo/sub`（`repo` 有 `.git`）启动 → 信任提示的目标是 `repo/sub`。
- `pkg/tui` 测试：打开会话后项目表里有 `root = repo/sub` 的项目，新会话的 `project_id` 指向它。

### Step 4：`/resume` 只列本项目

1. `ChatSession.ListSessionsRecent(Paged)`：`ProjectID` 非空时改用 `ListSessionsForProject`。核对它的过滤与排序（`session_store.go:768-805`）：要排除 subagent 与工坊会话（`source = ''`），排序与原列表一致，按更新时间倒序。不一致就按原列表的规则补齐，并补测试。
2. 列表为空时沿用今天的 "There is no other conversation to resume."。

**Verify**：同一主代理下建两个项目的会话，`/resume` 列表只有当前项目的。

### Step 5：`resume <id>` 指向别的项目 → 拒绝

`runStreamingTerminalWithInitialSessionID` 在 `initialSessionID` 非空时，于 Step 3 第 1 点之后：

1. 查该会话的项目（`ProjectStore.ProjectForSession`）：
   - 会话不存在 → 沿用今天的报错路径；
   - 项目根 ≠ 当前启动根 → 打印 `这个对话属于项目 <名称>（<根>），请在该目录运行：forebrain resume <id>`，以非 0 退出；
   - 项目根目录不存在 → 打印 `这个对话所在的目录已不存在：<根>。无法继续。`，以非 0 退出；
   - 会话没有项目：只可能是主代理级工坊会话（`source='workshop'`）→ 打印 `这个对话属于技能工坊，请在网页端打开。`，以非 0 退出。
2. 会话内的 `/resume` 选择器只会列本项目的会话（Step 4），所以不会走到这里。

**Verify**：上述三种情况各一个测试。

### Step 6：系统提示与显示里的项目根

1. `projectContextPreHook`（`chat_session.go:217`）：`memory.ProjectRoot(cwd)` → 当前项目根（`ChatSession` 上的根，等于启动根）。
2. `render.go:1960`：`memory.ProjectRoot(r.cwd)` → 渲染器拿得到的项目根。拿不到时用 `r.cwd`，因为 TUI 的进程目录就是项目根。
3. 改完后 `grep -n "memory.ProjectRoot" pkg/tui/*.go` 应无输出。

### Step 7：粘贴截图存进项目

`ReadClipboardImage`：
- 目标目录改为 `<项目根>/attachments/`，不存在就 `MkdirAll(0o755)`；
- 文件名规则不变（今天是 `forebrain-clipboard-<pid>-*.png`，保持）；
- 项目根从 `ChatSession` 取，所以 reader 需要拿到它：给 `systemClipboardImageReader` 加一个字段，在构造处传入（`grep -rn "DefaultClipboardImageReader()" pkg/tui cmd`）；
- 拿不到项目根（测试或无项目）时保持写临时目录。

**Verify**：测试用假剪贴板命令（看今天的测试怎么桩：`grep -rn "ReadClipboardImage" pkg/tui/*_test.go`），断言文件落在 `<root>/attachments/`。

### Step 8：真机（必须）

按仓库的 TUI driver 约定（隔离 home，key 从 `.env` 注入；见记忆 real-model-acceptance 与 `scripts/` 下的 driver），在临时目录 `repo/sub` 启动：

1. 信任提示的路径是 `repo/sub`；
2. 发一条消息让模型执行 `pwd`，输出是 `repo/sub`；
3. 退出后 `sqlite3 -readonly <隔离 home>/state/forebrain.state.sqlite "SELECT root FROM fb_projects"` 含 `repo/sub`，会话的 `project_id` 指向它；
4. 在 `repo` 启动，`/resume` 列表里**没有**第 1 步的会话；`forebrain resume <第 1 步的 id>` 被拒绝并提示 `repo/sub`；
5. 在隔离 home 的 `workspace` 子目录启动，被拒绝。

把每步的屏幕输出要点写进报告。

## Test plan

见各步 Verify 与 Step 8。

## Done criteria

- [ ] Commands 表全部通过
- [ ] `grep -n "memory.ProjectRoot" pkg/tui/*.go` 无输出；`grep -rn "func validateProjectRoot" pkg/gateway` 无输出
- [ ] Step 8 真机 5 项全部符合
- [ ] `git status --short` 中本计划新增的改动只涉及 Scope 列出的文件

## STOP conditions

1. Drift check 不符。
2. `ListSessionsForProject` 的语义与 `/resume` 原列表差得太多，补齐会影响 gateway 的项目会话列表（两者共用）。
3. 切换主代理后无法重新绑定项目默认值（store 生命周期不允许）。
4. 真机第 2 步 `pwd` 不是 `repo/sub`（说明 026/027 有缺口），停下报告，不要在本计划里补。

## Maintenance notes

- TUI 与 web 共用 `ValidateProjectRoot` 和 `FindOrCreateByRoot`，两端的项目口径只有一份。改规则时两端一起生效。
- 在仓库子目录启动现在是一个独立项目：信任、计划目录、项目记忆都以子目录为准。这是 owner 定的语义，不是回归。有用户反馈"子目录看不到仓库根的记忆"时，按这个语义解释。
