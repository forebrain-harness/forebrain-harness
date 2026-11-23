# Plan 120：工作区信任也挪进 TUI——先打开 TUI，再在面板里问「Trust this folder?」

> **执行者须知**：按步骤顺序执行，每步跑验证命令并确认预期结果后再进下一步。触发
> "STOP conditions" 时停下报告，不要即兴发挥。**不要更新 `plans/README.md`**（评审方维护索引）。
> **禁止提交代码**（owner 手动提交）：不 commit、不 push、不开 PR、不建分支、不用 worktree，改动直接留在 dev 工作区。
> `pkg/tui/run.go`、`pkg/tui/chat_session.go`、`pkg/tui/overlays.go`、`pkg/tui/render.go` **已有 owner 未提交的改动**：只做本计划的修改。
>
> **前置**：`plans/111` 与 `plans/112` 必须已经落地（本计划改的是同一段启动流程，并依赖「对话轮次只跑在信任决定之后才建的项目 runner 上」）：
>
> ```bash
> grep -c 'func startConversationRuntime(' pkg/tui/run.go          # 1
> grep -c 'askProjectServers(cmds, renderer)' pkg/tui/run.go        # 2（启动一处、切会话一处）
> test ! -f cmd/forebrain/mcp_consent.go && echo gone              # gone
> grep -c 'interactiveEnsureWorkspaceTrusted(cmd.InOrStdin(), cmd.OutOrStdout(), root, launchRoot)' cmd/forebrain/interactive.go   # 1
> ```
>
> 任何一行不符就 STOP 1。

## Status

- **Priority**: P2
- **Effort**: M
- **Risk**: MED（信任决定从进程启动前挪到启动后，冻结在环境里的派生配置要在决定后重新推导）
- **Depends on**: `plans/111-tui-conversations-run-on-project-runners.md`、`plans/112-tui-project-servers-panel.md`
- **Category**: direction（owner 2026-10-24）
- **Planned at**: commit `f18ff14`（dev 工作区含 owner 未提交改动），2026-10-24

## Why this matters

`112` 之后，启动前唯一剩下的确认页面是工作区信任：在一个新目录里运行 `forebrain`，先出一个独立的整屏选择页，选了才进 TUI。
owner 要求它也和项目服务确认一样：**TUI 先打开，再在 TUI 里问**。owner 选定的样子与语义（2026-10-24）：

```
 Trust this folder?
 Forebrain Harness can edit files here and follow the folder's own instructions and skills.

 Path  ~/workspace/chatibs_audit_agent

 ❯ Quit
   Trust and continue

 ↑/↓ to navigate · Enter to confirm · Esc to quit
```

- 只有两个选项，**光标默认停在 Quit**（信任必须主动选，不能被 Enter 顺手选中）；Esc 与 Ctrl+C 都等于 Quit。
- Quit：退出 TUI，**什么都不记、不登记项目**（与现在一样）。
- Trust and continue：记为信任，接着（如有）弹 `112` 的项目服务面板，然后进入会话。
- 已有决定（信任或网页上记过「不信任」）就不问；记过「不信任」的目录照常进入，靠更严的审批策略约束（现有语义不变）。

## Current state

**启动链路**（`cmd/forebrain/interactive.go`，`runStreamingTerminalWithInitialSessionID`，`112` 之后）：

```go
	caret := hideLaunchCaret(cmd.OutOrStdout())
	defer caret.restore()
	allowed, err := interactiveEnsureWorkspaceTrusted(cmd.InOrStdin(), cmd.OutOrStdout(), root, launchRoot)
	if err != nil {
		return err
	}
	if !allowed {
		return tui.ErrCancelled
	}
	restoreLog := tui.RedirectProcessLoggingForTUI(root)
	...（首次运行的入门向导 onboarding，独立页面）...
	sess, err := interactiveOpenChatSession(cmd.Context(), launchRoot)   // process.Open：这里把信任冻结进环境
	...
	if resumeID := strings.TrimSpace(initialSessionID); resumeID != "" {
		if err := sess.ResumeOnLaunchProject(cmd.Context(), resumeID); err != nil {
			return resumeRefusal(err)
		}
	} else {
		if err := sess.AdoptLaunchProject(cmd.Context()); err != nil {   // 在数据库里登记「启动目录」这个项目
			return err
		}
	}
	...
	caret.handOff()
	return interactiveTUIRun(cmd.Context(), tui.Options{...})
```

`tui.ErrCancelled` 被 `cmd/forebrain/root.go:36` 静默吞掉（退出码 0、不打印）。

**信任页本体**（`cmd/forebrain/workspace_trust.go`，整个文件）：`ensureWorkspaceTrusted`（有决定就放行，`LevelUnknown` 才问）、
`promptWorkspaceTrust`（用 `tui.NewStandaloneRawSelector` 开整屏页）、`askWorkspaceTrust(sel trustReviewer, target)`（调 `Review`，Quit 在前、默认 Quit）、
`workspaceTrustActions = {"Quit", "Trust and continue"}`、`workspaceTrustLabel`（现文案「Trust this directory?\nOnce trusted, … — trust only code you know.」）、
`workspaceTrustTarget(root)`（`safety.ResolveRegisteredProject(root).Root`，即启动目录本身，不向上找 `.git`）、`markWorkspaceTrusted`（`safety.MarkTrusted(home, safety.Project{Root: target})`）。
测试 `cmd/forebrain/workspace_trust_test.go`（6 个）与 `cmd/forebrain/interactive_test.go` 里桩掉 `interactiveEnsureWorkspaceTrusted` 的地方。
`NewStandaloneRawSelector` 还被入门向导（`pkg/tui/setup.go:50`）用，**保留**。

**信任被冻结在哪里**（`process.Open`，`pkg/process/open.go:297-337`）：`launchProject`（含 `TrustLevel`）→ `env.LaunchProject`、`env.Deps.LaunchProject`（基础 runner 的 `LaunchProject`）、
`cfgRoot := effectiveFor(loaded, launchProject)`（由信任推导沙箱模式与审批策略：未决定 ⇒ 只读沙箱 + on-request；信任或不信任 ⇒ workspace-write，不信任再加 untrusted 审批，
见 `pkg/safety/sandbox_config.go:567-592`）、`env.Sandbox`、技能服务的 `OnRefresh`。TUI 侧另有一份 `ChatSession.LaunchProject`（`pkg/tui/notify.go:1554` 从 `env.LaunchProject` 拷贝，
`chat_session.go:2504` 与 `chat_slash.go:264`、`:1037` 读它）。

**能复用的推导**：`(*Environment).reloadConfig`（`pkg/process/config_reload.go:191-280`）读配置文件后 `next := effectiveFor(loaded, env.LaunchProject)`，
再 `env.Runner.LoadConfig(next)`、`env.Deps.AppCfg`、`env.LSP.Reconcile`、池 `PropagateConfig`、`refreshSandboxRuntimeLocked`、规则/上下文钩子、技能服务刷新、`OnConfigReload`。
也就是说：**先改 `env.LaunchProject`，再跑一遍这段推导**，冻结的派生配置就全部对齐到新的信任。

**为什么在 TUI 里决定是安全的**：决定之前环境按「未决定」推导（只读沙箱）；项目钩子 `SessionStart` 要到第一次发消息才触发（`pkg/tui/chat_surface.go:53`）；
`112` 的项目服务面板在未信任时根本没有待确认项；`111` 之后对话轮次只跑在项目 runner 上，而项目 runner 在 `startConversationRuntime` 里才建、建的时候现读信任
（`pkg/process/runner_pool.go` 的 `buildEntryLocked` 调 `safety.ResolveRegisteredContext`）。**唯一的副作用是 `AdoptLaunchProject` 登记项目**——所以它必须挪到信任决定之后。

**`Review` 的 Esc 提示**：`(*rawSelector).Review`（`pkg/tui/overlays.go:4800`）的提示用 `s.escHint()`，在 TUI 里是 `Esc to cancel`（只有独立页面才是 `Esc to quit`，`render.go:6204-6213`）。
信任面板需要 `Esc to quit`。

**`112` 之后 TUI 的启动顺序**（`pkg/tui/run.go` 的 `Run`）：欢迎界面 → `printSessionResumeContext`（resume 时）→ `askProjectServers(cmds, renderer)` → `startConversationRuntime(...)` → `StartApprovalRecovery`。

## Commands you will need

| Purpose | Command | Expected |
|---|---|---|
| Build | `go build ./...` | exit 0 |
| 单测 | `go test -tags fts5 ./pkg/process/ ./pkg/tui/ ./cmd/forebrain/` | `ok` ×3 |
| 护栏 | `go test ./pkg/architecture/` | `ok` |
| 格式 | `gofmt -l pkg/process pkg/tui cmd/forebrain` | 无输出 |
| 包图 | `scripts/package-graph.sh && git diff -- pkg/architecture/testdata/graph.json \| grep '^[+-]' \| grep -v '"loc"' \| grep -v '^[+-][+-]'` | 无输出 |

## Scope

**In scope**：`pkg/process/config_reload.go`、`pkg/process/*_test.go`（新用例）、`pkg/tui/run.go`、`pkg/tui/chat_session.go`、`pkg/tui/overlays.go`（只加 Review 的变体）、
`pkg/tui/workspace_trust.go`（新建）、`pkg/tui/workspace_trust_test.go`（新建）、`cmd/forebrain/interactive.go`、`cmd/forebrain/interactive_test.go`、
删除 `cmd/forebrain/workspace_trust.go` 与 `cmd/forebrain/workspace_trust_test.go`、`pkg/architecture/testdata/graph.json`（脚本生成）。

**Out of scope**：入门向导（仍在 TUI 之前，独立页面）；网页项目页的信任开关；`safety` 的信任存储格式；`ResumeOnLaunchProject` 的拒绝逻辑（仍在 TUI 之前——它是「这个会话不属于这里」的报错退出）。

## Steps

### Step 1：环境能在启动后采纳新的信任决定

`pkg/process/config_reload.go`：

1. 把 `reloadConfig` 里「`loaded, err := appcfg.Load(...)` 之后的全部」抽成 `func (env *Environment) applyRawConfigLocked(loaded appcfg.Root) error`（调用方持有 `reloadMu`）；
   `reloadConfig` 变成：加锁 → 读文件 → `applyRawConfigLocked(loaded)`。**行为不变**，现有热重载测试必须照样通过。
2. 新增：

   ```go
// AdoptLaunchTrust re-reads the launch project's trust decision and
// re-derives everything the environment froze from it — the base runner's
// effective sandbox and approval policy, the sandbox manager, the skills
// service — from the configuration it already holds. The interactive surface
// calls it after the operator answers the trust question inside the TUI. A
// process with no launch project has nothing to re-read.
func (env *Environment) AdoptLaunchTrust() error
   ```

   实现：`env == nil` → 错误；`env.noLaunchProject` 或 `env.LaunchProject.Project.Root == ""` → `nil`；加 `reloadMu`；
   `launch, err := safety.ResolveRegisteredContext(env.Root, env.LaunchProject.Project.Root)`，出错返回；
   `env.LaunchProject = launch`；`env.Deps.LaunchProject = launch`；若 `env.Runner != nil && env.Runner.Deps != &env.Deps` 也设 `env.Runner.Deps.LaunchProject = launch`；
   然后 `return env.applyRawConfigLocked(env.RawConfig)`。

**Verify**：`go test -tags fts5 ./pkg/process/` → `ok`（含既有热重载用例）。

### Step 2：会话侧的信任读写

`pkg/tui/chat_session.go` 加：

```go
// WorkspaceTrustPending reports whether the launch folder still needs a trust
// decision, and which folder that is. A recorded decision either way — trusted,
// or declined on the project page — ends the question.
func (s *ChatSession) WorkspaceTrustPending() (folder string, pending bool, err error)

// TrustWorkspace records the launch folder as trusted and has the environment
// adopt the decision, so what was derived from "not decided yet" (a read-only
// sandbox among it) is derived again.
func (s *ChatSession) TrustWorkspace() error
```

- `WorkspaceTrustPending`：`folder := strings.TrimSpace(s.LaunchProject.Project.Root)`，空就 `("", false, nil)`；
  `level, err := safety.TrustLevel(s.home(), safety.Project{Root: folder})`；`pending = level == safety.LevelUnknown`。
- `TrustWorkspace`：`safety.MarkTrusted(s.home(), safety.Project{Root: folder})`；`s.Env.AdoptLaunchTrust()`；成功后 `s.LaunchProject = s.Env.LaunchProject`，再 `s.refreshSandboxRuntime()`。

（`s.LaunchProject.Project.Root` 与旧 `workspaceTrustTarget(launchRoot)` 是同一个目录：两者都是 `ResolveRegisteredProject` 的规范路径，`Open` 用 `ExactLaunchRoot` 解析。Step 5 的测试要钉住这一点。）

**Verify**：`go build ./...` → exit 0。

### Step 3：TUI 里的信任面板

1. `pkg/tui/overlays.go`：把 `Review` 的实现挪进 `func (s *rawSelector) reviewWith(label string, facts []turn.StatusFact, actions []string, defaultIdx int, escHint string) (int, bool, error)`，
   `Review` 改为 `return s.reviewWith(label, facts, actions, defaultIdx, s.escHint())`（行为不变）。
2. 新建 `pkg/tui/workspace_trust.go`：

   ```go
// The trust question, asked inside the TUI once it is on screen: whether the
// folder it started in may be worked in. Quit comes first and is where the
// panel opens — trusting a folder has to be chosen, not fallen into with
// Enter — and Esc and Ctrl+C both answer Quit.

const (
	workspaceTrustQuit = iota
	workspaceTrustAccept
)

var workspaceTrustActions = []string{
	workspaceTrustQuit:   "Quit",
	workspaceTrustAccept: "Trust and continue",
}

const workspaceTrustLabel = "Trust this folder?\n" +
	"Forebrain Harness can edit files here and follow the folder's own instructions and skills."

// workspaceTruster is the session half; optional so fake sessions need not offer it.
type workspaceTruster interface {
	WorkspaceTrustPending() (folder string, pending bool, err error)
	TrustWorkspace() error
}

// workspaceTrustReviewer is the selector half; the interactive raw selector implements it.
type workspaceTrustReviewer interface {
	ReviewWorkspaceTrust(folder string) (trusted bool, err error)
}

// askWorkspaceTrust asks when the launch folder has no decision yet and
// records a trust. It reports whether the surface must quit.
func askWorkspaceTrust(cmds *commandController, renderer *Renderer) (quit bool, err error)
   ```

   - `(s *rawSelector) ReviewWorkspaceTrust(folder)`：`idx, ok, err := s.reviewWith(workspaceTrustLabel, []turn.StatusFact{{Label: "Path", Value: displayFolder(folder)}}, workspaceTrustActions, workspaceTrustQuit, "Esc to quit")`；
     `err` 原样返回；其余返回 `ok && idx == workspaceTrustAccept`。`Path` 的值把 `$HOME` 前缀显示成 `~`（看看 `startupWorkingDirectoryDisplay` 是否已有这个逻辑，有就复用）。
   - `askWorkspaceTrust`：会话不实现 `workspaceTruster` 或 selector 不实现 `workspaceTrustReviewer` → `(false, nil)`；`pending` 为 false → `(false, nil)`；
     查询出错 → `(true, err)`（读不出信任就不进，fail closed）；`trusted, err := reviewer.ReviewWorkspaceTrust(folder)`：err → `(true, err)`；`!trusted` → `(true, nil)`；
     `TrustWorkspace()` 出错 → `(true, err)`；成功 → `(false, nil)`。

**Verify**：`go build ./...` → exit 0。

### Step 4：接进启动流程，删掉启动前的信任页

1. `pkg/tui/run.go` 的 `Run`：在欢迎界面 / `printSessionResumeContext` 之后、`askProjectServers(cmds, renderer)` **之前**：

   ```go
	// The trust question is asked once the TUI is on screen. Until it is
	// answered the environment stands on "not decided" (a read-only
	// sandbox), nothing the folder declares has run, and no conversation
	// runtime exists yet — the project runner is built below, after it.
	if quit, err := askWorkspaceTrust(cmds, renderer); quit {
		if err != nil {
			return err
		}
		return ErrCancelled
	}
   ```

   紧接着（仍在 `askProjectServers` 之前）登记项目——只对新启动（`strings.TrimSpace(opts.InitialSessionID) == ""`）：
   `if a, ok := opts.Session.(launchProjectAdopter); ok { if err := a.AdoptLaunchProject(ctx); err != nil { return err } }`，
   其中 `type launchProjectAdopter interface{ AdoptLaunchProject(ctx context.Context) error }` 放在 `conversationRuntimeBinder` 旁边，注释：项目只在目录被信任（或早有决定）之后才登记，选 Quit 不留下任何记录。
   确认 `return ErrCancelled` 走的是 `Run` 现有的 defer 收尾（退出视口、恢复终端）；`sessionTouched` 为 false，所以不会打印 resume 提示。
2. `cmd/forebrain/interactive.go`：删 `interactiveEnsureWorkspaceTrusted` 变量与那段调用（`allowed` 判断与 `return tui.ErrCancelled`）；
   `else { sess.AdoptLaunchProject(...) }` 分支删除（挪进了 TUI），`resume` 分支的 `ResumeOnLaunchProject` 保留；改正 `:68-72` 讲「trust, MCP/LSP consent」的注释与 `launchCaret` 注释（TUI 之前只剩入门向导与报错）。
3. 删除 `cmd/forebrain/workspace_trust.go`。`interactive_test.go`：`stubInteractiveLaunch` 里删 `interactiveEnsureWorkspaceTrusted` 的保存/还原/桩；
   `TestRunStreamingTerminalKeepsCaretHiddenUntilTheTUI` 里桩信任页的那段删掉（保留它对入门向导与 TUI 交接的断言）；删 `TestRunStreamingTerminalRestoresCaretWhenTrustDeclined`（这个路径不存在了）。
4. 删除 `cmd/forebrain/workspace_trust_test.go`，它的语义在 Step 5 搬到 `pkg/tui`。

**Verify**：`go build ./... && go test -tags fts5 ./cmd/forebrain/ ./pkg/tui/` → `ok` ×2；
`grep -rn 'interactiveEnsureWorkspaceTrusted\|ensureWorkspaceTrusted\|promptWorkspaceTrust' cmd pkg` → 无输出。

### Step 5：单测

见 Test plan。

**Verify**：`go test -tags fts5 ./pkg/process/ ./pkg/tui/ ./cmd/forebrain/` → `ok` ×3；`go test ./pkg/architecture/` → `ok`；`gofmt` 无输出；包图只有 `loc` 变化。

### Step 6：真机验收（必须做）

用 `run-forebrain` 技能的 `driver.sh`（或 `mcp_acc.sh`，把工作目录指到你的临时目录）在一个**全新**目录启动：

1. 先看到 TUI 欢迎界面，再出现信任面板，文案与上图逐字一致，`❯` 在 Quit 上；终端里**没有**独立的整屏信任页。
2. 直接 Enter（Quit）→ 回到 shell，退出码 0；`sqlite3 <home>/state/forebrain.state.sqlite "SELECT count(*) FROM fb_projects"` 为 0；信任存储里没有这个目录。
3. 再启动，Esc → 同 2。Ctrl+C → 同 2。
4. 再启动，↓ Enter（Trust and continue）→ 进入输入框；`fb_projects` 有这个目录；发一句话正常回复；`!touch x` 能写成功（证明沙箱已从只读刷新为可写）。
5. 有项目服务文件时（复用 `112` Step 7 的 `.forebrain/mcp_servers.yaml`）：信任之后紧接着出现项目服务面板。
6. 退出重启 → 不再出现信任面板。把该目录在网页项目页设为「不信任」后重启 → 不出现面板、直接进入（审批更严）。
7. 用 Terminal.app（Fira Code 14pt）实看一次面板外观，无越界、无残影。

## Test plan

模式参照：`pkg/tui/overlays_test.go` 的 `pickerKeys`（Esc 只能放在按键序列最后，见 `112` 的说明）；`pkg/process/config_reload_test.go`（或现有热重载测试所在文件，`grep -rln "reloadConfig\|ReloadConfig" pkg/process/*_test.go`）；
`cmd/forebrain/workspace_trust_test.go` 的 `writeUntrustedProject`（记录拒绝的写法，搬过去用）。

1. `pkg/process`：`TestAdoptLaunchTrustRederivesTheSandbox`——在未决定的目录 `Open`（`ExactLaunchRoot`），断言有效配置的沙箱是只读；`safety.MarkTrusted` 后 `AdoptLaunchTrust()` →
   `env.LaunchProject.TrustLevel == LevelTrusted`、`env.Deps.AppCfg.SandboxMode == workspace-write`（或以 `safety` 暴露的读法断言）；`noLaunchProject` 的环境调用返回 nil 且不变。
2. `pkg/process`：抽出 `applyRawConfigLocked` 后，现有热重载用例全部通过（不新增，跑一遍即可）。
3. `pkg/tui/workspace_trust_test.go`：
   - 面板打开在 Quit：用记录参数的假 reviewer（照 `cmd/forebrain/workspace_trust_test.go` 的 `fakeTrustReviewer` 写法）或直接 `pickerKeys(t, keyEnter)` + `ReviewWorkspaceTrust` → `false`。
   - `pickerKeys(t, keyDown+keyEnter)` → `true`；Esc（`"\x1b"`）→ `false`；Ctrl+C（`"\x03"`）→ `false`。
   - 画面：标题 `Trust this folder?`、副标题一句、`Path` 行、两个动作、提示末尾是 `Esc to quit`。
   - 编排：未决定 + Quit → `quit == true` 且 `TrustWorkspace` 未被调用；未决定 + Trust → `TrustWorkspace` 调一次、`quit == false`；已决定（含记录过拒绝）→ 不问；
     查询出错 → `quit == true` 且返回错误。
   - `TestWorkspaceTrustFolderIsTheLaunchDirectory`：在 `git init` 过的仓库的子目录开会话（参照被删测试 `TestWorkspaceTrustTargetIsTheLaunchDirectory`），`WorkspaceTrustPending` 报的是子目录本身。
4. `pkg/tui`：`Run` 的启动顺序——用一个同时实现 `workspaceTruster`、`launchProjectAdopter`、`projectServersConsenter`、`conversationRuntimeBinder` 的假会话记录调用顺序（若 `Run` 难以在单测里驱动，就把这几步抽成一个
   `startupQuestions(ctx, …)` 函数单独测）：顺序必须是 trust → adopt → project servers → bind；Quit 时后三者都不发生。

## Done criteria

- [ ] `go build ./...`；`go test -tags fts5 ./pkg/process/ ./pkg/tui/ ./cmd/forebrain/` 全 `ok`，含上述新用例
- [ ] `go test ./pkg/architecture/` `ok`；`gofmt` 无输出；包图只有 `loc` 变化
- [ ] `test ! -f cmd/forebrain/workspace_trust.go && test ! -f cmd/forebrain/workspace_trust_test.go`
- [ ] `grep -rn 'Trust this directory' cmd pkg` 无输出；`grep -rn 'Trust this folder?' pkg/tui` 有输出
- [ ] Step 6 七项逐一通过（报告里列出观察结果，含 `fb_projects` 计数）

## STOP conditions

1. 前置检查不符（`111` / `112` 未落地或已漂移）。
2. `AdoptLaunchTrust` 之后发现还有冻结的派生值没刷新（例如 `!` 命令仍被只读沙箱拦下、`/permissions` 仍显示只读）——列出来源停下，不要逐个打补丁。
3. 在 TUI 里 `return ErrCancelled` 没有恢复终端（光标消失、留在备用屏）。
4. 去掉启动前信任页后，`process.Open` 在未决定的目录里报错（例如 `StartupCheck` 对只读推导失败）。
5. 有别的代码依赖 `AdoptLaunchProject` 在 `tui.Run` 之前完成（例如 `Run` 早期就读 `s.ProjectID`）。

## Maintenance notes

- 启动时 TUI 里的问题顺序固定为：信任 → 登记项目 → 项目服务确认 → 建项目 runner。以后在启动时加任何问题，都要想清楚它在这条链上的位置；**任何会在目录里执行东西的步骤都必须在信任之后**。
- 信任决定之后的「重新推导」只走 `AdoptLaunchTrust` → `applyRawConfigLocked` 这一条路；新增任何从 `env.LaunchProject` 派生并缓存的东西，都要放进 `applyRawConfigLocked`，否则信任改了它不会跟着变。
- 面板文案是 owner 选定的；改字前先问 owner。
