# Plan 112：TUI 先打开，再用一屏勾选面板一次确认项目的 MCP 与语言服务器（不再逐条问）

> **执行者须知**：按步骤顺序执行，每步跑验证命令并确认预期结果后再进下一步。触发
> "STOP conditions" 时停下报告，不要即兴发挥。**不要更新 `plans/README.md`**（评审方维护索引）。
> **禁止提交代码**（owner 手动提交）：不 commit、不 push、不开 PR、不建分支、不用 worktree，
> 改动直接留在 dev 工作区。`pkg/tui/run.go`、`pkg/tui/chat_session.go`、`pkg/tui/chat_session_test.go`
> **已有 owner 未提交的改动**：只做本计划描述的修改，不要回滚、重排或格式化别的代码。
>
> **前置**：`plans/110` 与 `plans/111` 必须已经落地。先确认：
>
> ```bash
> grep -c 'func DecideProjectServers(agentWorkspace string, launch safety.ProjectContext, d ProjectServersDecision) error' pkg/process/mcp.go   # 1
> grep -c 'Detail string `json:"-"`' pkg/mcp/project_consent.go pkg/lsp/project.go                                                           # 各 1
> grep -c 'func startConversationRuntime(' pkg/tui/run.go                                                                                    # 1
> grep -c 'func askProjectMCPConsentBeforeSwitch(cmds \*commandController, renderer \*Renderer)' pkg/tui/run.go                              # 1
> grep -c 'interactiveEnsureProjectMCPConsent(cmd.InOrStdin(), cmd.OutOrStdout(), root, launchRoot)' cmd/forebrain/interactive.go           # 1
> test -f cmd/forebrain/mcp_consent.go && echo present                                                                                      # present
> ```
>
> 任何一行不符就 STOP 1（前置没落地或代码已漂移）。

## Status

- **Priority**: P1（owner 直接提的体验问题）
- **Effort**: M
- **Risk**: MED（改启动流程与切会话前的确认；判定逻辑全在 `110`）
- **Depends on**: `plans/110-project-servers-consent-engine.md`、`plans/111-tui-conversations-run-on-project-runners.md`
- **Category**: direction（owner 2026-10-23）
- **Planned at**: commit `f18ff14`（dev 工作区含 owner 未提交改动），2026-10-23

## Why this matters

现在 `forebrain` 启动时，**还没进 TUI** 就在普通终端里逐条问：

```
This project declares MCP servers. Confirm each one before it runs in this session.

caveman-shrink: runs `npx -y caveman-shrink codegraph serve --mcp`
Run it? [y/N] y
  confirmed — it loads this session
```

语言服务器再用同样的方式问一遍（`Allow it? [y/N]`）。owner 要求（2026-10-23，参照 Claude Code 与 Codex）：

1. **先打开 TUI**，再在 TUI 里显示确认面板；
2. **不逐条确认**：一屏列出全部待确认项（MCP 与语言服务器同一个面板），**默认全部勾选**，空格取消个别，Enter 一次确认；勾着的=允许，没勾的=不允许（记下，配置不变就不再问）；
3. **Esc = 全部不允许**（记下）；Ctrl+C = 什么都不记、下次再问；
4. 面板里有「**Always allow this project's servers**」——勾上后这个项目以后新增或改动的服务都不再问；
5. MCP 与 LSP 语义完全一致；文案要语义化、友好、**简洁**（不加解释性句子）。

`/new`、`/resume`、`/fork` 进入会话前现有的一套 TUI 内逐条确认（`askProjectMCPConsentBeforeSwitch`，只管 MCP、文案冗长）也换成同一个面板。

## 面板（owner 已选定的样子，照此实现）

```
 Project servers
 3 servers want to run in this project

 MCP
 ❯ ● caveman-shrink  npx -y caveman-shrink codegraph serve --mcp
   ● github          https://api.example.com/mcp
 Language servers
   ● gopls           gopls serve

   ○ Always allow this project's servers

 ↑/↓ to navigate · Space to select · Enter to confirm · Esc to decline all
```

文案逐字如下（不要增删句子）：

| 位置 | 文案 |
|---|---|
| 标题 | `Project servers` |
| 副标题 | 1 项：`1 server wants to run in this project`；N 项：`N servers want to run in this project` |
| 组标题 | `MCP`、`Language servers`（某组没有条目就不画该组） |
| 行 | 左列名字，右列 `Detail`（`110` 加的字段：命令行 / 地址 / `changes built-in gopls settings` / `… · turns it on`） |
| 开关行 | `Always allow this project's servers` |
| 提示 | `↑/↓ to navigate · Space to select · Enter to confirm · Esc to decline all` |

勾选符号沿用多选面板的 `●`（选中）/ `○`（未选），常量 `selectorCheckedGlyph` / `selectorUncheckedGlyph`（`pkg/tui/overlays.go`）。

交互规则：

- 打开时：所有条目 `●`，开关 `○`，光标在第一条。没有输入框（不显示过滤条，也没有文字光标；见 `tui-picker-focus-model` 约定：❯ 与光标互斥）。
- ↑/↓ 在条目与开关行之间移动（夹在两端，不循环）。
- 空格在条目上：翻转该条；**若翻成未选，开关同时变 `○`**。空格在开关行上：翻转开关；**翻成 `●` 时所有条目同时变 `●`**。（不变式：开关开着 ⇒ 全部勾选。）
- Enter：按当前勾选记录（勾着的 allow、没勾的 deny），开关开着就同时打开项目的「总是允许」。
- Esc：所有列出的条目记为 deny，开关不动。
- Ctrl+C：关面板，什么都不记。
- 鼠标事件交给 `s.dispatchMouse`（与 `Review` 一致），不做点击勾选。

## Current state

**启动前的逐条提示**（要删掉）：

- `cmd/forebrain/interactive.go:90-101`：

  ```go
	// Project-level MCP entries are confirmed right after the trust prompt:
	// the list is frozen for the session, so startup is the only moment to
	// ask. Anything left unconfirmed stays out of the session.
	if err := interactiveEnsureProjectMCPConsent(cmd.InOrStdin(), cmd.OutOrStdout(), root, launchRoot); err != nil {
		return err
	}
	// The project's language-server entries follow the same rule, asked
	// right after the MCP ones (spec §5.2): an unconfirmed entry does not
	// apply, not even as an override of a built-in server.
	if err := interactiveEnsureProjectLSPConsent(cmd.InOrStdin(), cmd.OutOrStdout(), root, launchRoot); err != nil {
		return err
	}
  ```

  以及 `:32-33` 的两个可替换变量 `interactiveEnsureProjectMCPConsent` / `interactiveEnsureProjectLSPConsent`，和 `launchCaret` 注释（`:205-216`）里
  「The one line typed at the caret, the project-MCP confirmation, shows it while it waits for the answer.」这一句。
- `cmd/forebrain/mcp_consent.go`：整个文件只有 `ensureProjectMCPConsent` 与 `ensureProjectLSPConsent`（逐条 `[y/N]`）。
- `cmd/forebrain/interactive_test.go`：`stubInteractiveLaunch`（`:175-210`）存/还原/桩掉这两个变量；
  `TestInteractiveAsksProjectLSPConsentAfterMCP`、`TestEnsureProjectLSPConsentPrompt`、`TestEnsureProjectMCPConsentPrompt`、
  `TestEnsureProjectLSPConsentRegisteredProjectBoundary` 四个测试只测这两个函数。
- `cmd/forebrain/lsp.go:100` 的注释提到「like mcp_consent.go」。
- `pkg/process/open.go` 的 `ResolveConsentProjectContext` 与 `consentRegisteredRoot`：**只**被 `cmd/forebrain/mcp_consent.go` 用（另有测试 `pkg/process/open_test.go` 的
  `TestResolveConsentProjectContextRegisteredWins`）。注意：它按「包含启动目录的已登记项目」记录决定，而 TUI 会话按**启动目录本身**解析项目——
  旧提示在子目录启动时问的是父项目的条目，会话却从不加载它们。面板改用会话自己的 `LaunchProject`，这个不一致随之消失。

**TUI 里已有的逐条确认**（要替换，`pkg/tui/run.go`）：

```go
type projectMCPConsenter interface {           // 约 :4094-4103
	PendingProjectMCPConsents() []mcp.PendingProjectConsent
	DecideProjectMCPConsents(allowed []string) error
	ReconcileSessionMCP() error
}

func askProjectMCPConsentBeforeSwitch(cmds *commandController, renderer *Renderer) {   // 约 :4689-4734
	...
	for _, entry := range pending {
		label := "Project MCP: " + strings.TrimSpace(entry.Summary) +
			"\nRun it in this project? The answer is remembered for this project; change it later on the project page's MCP tab."
		items := []SelectItem{
			{Label: "Allow it to run", Description: "It starts in this conversation and in every later one."},
			{Label: "Keep it out", Description: "It does not run; you can allow it later on the project page's MCP tab."},
		}
		idx, okSel, err := cmds.selector.SelectRich(label, items, 0)
		...
```

调用处在 `applySlashOutcome`（`outcome.SessionSwitched` 分支，`switchStreamSession` 之前）。
`ReconcileSessionMCP` 在 `projectMCPConsenter` 里，但 TUI 里没人用它。

`pkg/tui/chat_session.go:570-595` 的 `PendingProjectMCPConsents()` / `DecideProjectMCPConsents(allowed)`：用 `s.runner().WorkspaceRoot` 与 `s.deps().LaunchProject`
调 `process` 的同名函数。**沿用这两个来源**（主代理工作区、启动项目）。

**`111` 之后的启动顺序**（`pkg/tui/run.go` 的 `Run`）：欢迎界面 → `printSessionResumeContext`（resume 时）→ `startConversationRuntime(...)`（绑定项目 runner、它的 `Load` 启动 MCP）→
`StartApprovalRecovery`。面板插在 `startConversationRuntime` **之前**：先记下决定，再建 runner，新允许的服务就在第一次启动里。
切会话时同理：面板在 `switchStreamSession` 之前，`switchStreamSession` 里的 `startConversationRuntime` 会对项目 runner 做 `ReconcileSessionMCP`，把新允许的启动起来。

**范例**：`(*rawSelector).Review`（`pkg/tui/overlays.go:4800-4844`）是「抢输入 → 画面板 → 读键循环」的最小写法；
`tabbedPickerState.panel`（`pkg/tui/tabbed_picker.go:260-289`）示范了 `pickerHead` + `selectableColumns(selected, lead, name, nameWidth, desc)` + `●/○` lead；
`panelBuilder.group(heading)`（`pkg/tui/render.go:7017`）画组标题。按键常量：`rawKeyUp`、`rawKeyDown`、`rawKeySpace`、`rawKeyEnter`、`rawKeyEscape`、`rawKeyCtrlC`、`rawKeyMouse`、`rawKeyNone`。
`s.dismissed(rawKeyCtrlC)` 在 TUI 内返回 nil。

**架构护栏**：不要给 `pkg/tui` 加新的 `pkg/` import（`fanOutBudgets["tui"]` = 19，只许变小）；需要的类型都经 `process` 拿。

## Commands you will need

| Purpose | Command | Expected |
|---|---|---|
| Build | `go build ./...` | exit 0 |
| 单测 | `go test -tags fts5 ./pkg/tui/ ./cmd/forebrain/ ./pkg/process/` | `ok` ×3 |
| 护栏 | `go test ./pkg/architecture/` | `ok` |
| 格式 | `gofmt -l pkg/tui cmd/forebrain pkg/process` | 无输出 |
| 包图 | `scripts/package-graph.sh && git diff -- pkg/architecture/testdata/graph.json \| grep '^[+-]' \| grep -v '"loc"' \| grep -v '^[+-][+-]'` | 无输出 |

## Suggested executor toolkit

- `run-forebrain` 技能：Step 7 的真机验收；画面像素问题用它的 Terminal.app 回放（owner 用 Terminal.app + Fira Code 14pt）。

## Scope

**In scope**：
- `pkg/tui/project_servers.go`（新建）、`pkg/tui/project_servers_test.go`（新建）
- `pkg/tui/run.go`（删 `projectMCPConsenter`、`askProjectMCPConsentBeforeSwitch`；改调用处；启动处加一行）
- `pkg/tui/chat_session.go`（两方法换成新的两方法）、`pkg/tui/chat_session_test.go`（改 `consentSession` 与一个测试）
- `cmd/forebrain/interactive.go`、`cmd/forebrain/interactive_test.go`、`cmd/forebrain/lsp.go`（只改 `:100` 注释）
- 删除 `cmd/forebrain/mcp_consent.go`
- `pkg/process/open.go` / `pkg/process/open_test.go`（删 `ResolveConsentProjectContext`、`consentRegisteredRoot` 及其测试——仅当 Step 1 的 grep 证明再无调用方）
- `pkg/architecture/testdata/graph.json`（只由脚本重新生成）

**Out of scope**：
- 工作区信任页（`cmd/forebrain/workspace_trust.go`）——它决定能否打开这个目录，仍在 TUI 之前。
- 网页（会话里的确认弹窗、项目页开关）——`113`。
- `/mcp`、`/lsp` 面板——不加确认入口（被 Esc 全部拒绝的条目在网页项目页撤回）。
- `process` 的判定逻辑——全在 `110`，这里只调用。

## Steps

### Step 1：删掉启动前的逐条提示

1. `cmd/forebrain/interactive.go`：删 `:90-101` 两段调用与 `:32-33` 两个变量；`launchCaret` 注释删掉讲项目 MCP 确认的那一句（其余保持）。
2. `cmd/forebrain/interactive_test.go`：`stubInteractiveLaunch` 里删这两个变量的保存/还原/桩；删上面列出的四个测试。
3. 删除 `cmd/forebrain/mcp_consent.go`。
4. `cmd/forebrain/lsp.go:100` 注释改为不提那个文件（例如「the subcommands fail closed — no project, no trust, nothing starts」保留，删掉 `like mcp_consent.go,`）。
5. `grep -rn 'ResolveConsentProjectContext\|consentRegisteredRoot' --include='*.go' .` —— 若只剩 `pkg/process/open.go` 的定义与 `pkg/process/open_test.go` 的测试，
   就删除这两个函数、`TestResolveConsentProjectContextRegisteredWins`，以及因此不再使用的 import；否则 STOP 4。

**Verify**：`go build ./... && go test -tags fts5 ./cmd/forebrain/ ./pkg/process/` → `ok` ×2；
`grep -rn 'Run it? \[y/N\]\|Allow it? \[y/N\]' cmd pkg` → 只剩 `cmd/forebrain/lsp.go`（`lsp install` 的命令行确认，无关）。

### Step 2：会话侧——列出待确认项、记录答案

`pkg/tui/chat_session.go`：删 `PendingProjectMCPConsents()`、`DecideProjectMCPConsents(allowed)`，在原位置加：

```go
// PendingProjectServers lists the project's MCP servers and language servers
// that nobody has decided about yet — MCP first, in file order, then language
// servers by id — for the project-servers panel.
func (s *ChatSession) PendingProjectServers() []projectServerItem

// DecideProjectServers records the panel's answer for exactly the items it
// showed, bound to the fingerprints they had.
func (s *ChatSession) DecideProjectServers(items []projectServerItem, answer projectServersAnswer) error
```

- `PendingProjectServers`：`r := s.runner()`、`deps := s.deps()`，任一 nil 返回 nil；
  `process.PendingProjectMCPConsents(r.WorkspaceRoot, deps.LaunchProject)` 每项转 `projectServerItem{Kind: projectServerMCP, Name: e.Name, Detail: e.Detail, Fingerprint: e.Fingerprint}`；
  `process.PendingProjectLSPConsents(...)` 每项转 `Kind: projectServerLSP, Name: e.ID, ...`。
- `DecideProjectServers`：按 `answer.Allowed[i]` 拼 `process.ProjectServersDecision{MCP: ..., LSP: ..., AlwaysAllow: answer.AlwaysAllow}`
  （`process.ProjectServerChoice{Name, Fingerprint, Allow}`），调 `process.DecideProjectServers(r.WorkspaceRoot, deps.LaunchProject, d)`。
  `len(answer.Allowed) != len(items)` 时返回错误（防御，不该发生）。

（`projectServerItem` 等类型在 Step 3 定义；两步一起编译。）

### Step 3：面板本体

新建 `pkg/tui/project_servers.go`，文件头注释说明：项目的 MCP 与语言服务器条目随仓库而来，没有决定的条目在一个面板上一次确认；Esc 全部不允许，Ctrl+C 不记。内容：

```go
type projectServerKind int

const (
	projectServerMCP projectServerKind = iota
	projectServerLSP
)

// projectServerItem is one entry the panel asks about.
type projectServerItem struct {
	Kind        projectServerKind
	Name        string
	Detail      string
	Fingerprint string
}

// projectServersAnswer is what the panel returns. Decided false means it was
// closed without an answer (Ctrl+C) and nothing is recorded; otherwise
// Allowed holds one entry per item shown.
type projectServersAnswer struct {
	Decided     bool
	Allowed     []bool
	AlwaysAllow bool
}

// projectServersConsenter is the session half: the pending entries and a way
// to record the answer. Optional, so a fake session need not offer it.
type projectServersConsenter interface {
	PendingProjectServers() []projectServerItem
	DecideProjectServers(items []projectServerItem, answer projectServersAnswer) error
}

// projectServersReviewer is the selector half — the interactive raw selector
// implements it; a line selector does not, and then nothing is asked.
type projectServersReviewer interface {
	ReviewProjectServers(items []projectServerItem) (projectServersAnswer, error)
}
```

1. 纯状态 `projectServersPanelState{items, checked []bool, always bool, cursor int}`：`newProjectServersPanelState(items)`（全勾、开关关、光标 0）、
   `move(delta)`（夹在 `[0, len(items)]`，`len(items)` 是开关行）、`toggle()`（按上面「交互规则」的两条联动）、`answer() projectServersAnswer`（`Decided: true`）、
   `panel() slashPanel`（按上面的表画；`nameWidth` = 名字的最大 `displayLineWidth`；组标题用 `b.group`；开关行前 `b.blank()` 再 `b.selectable(...)`）。
2. `func (s *rawSelector) ReviewProjectServers(items []projectServerItem) (projectServersAnswer, error)`：照 `Review` 的结构——
   `acquireComposerInput`、`defer release()`、`s.showPanel(st.panel())`、读键循环：`rawKeyNone` 继续；`rawKeyMouse` → `s.dispatchMouse`；
   ↑/↓ → `move`；空格 → `toggle`；Enter → `return st.answer(), nil`；Esc → `return projectServersAnswer{Decided: true, Allowed: make([]bool, len(items))}, nil`；
   Ctrl+C → `return projectServersAnswer{}, s.dismissed(key.kind)`；其他键忽略；每次变化后 `s.showPanel(st.panel())`。
3. 编排函数：

   ```go
// askProjectServers asks about the project's undecided MCP and language-server
// entries on one panel and records the answer. Files that change under the
// open panel make the record fail as stale; the panel is then shown again with
// what is still pending, at most three times.
func askProjectServers(cmds *commandController, renderer *Renderer)
   ```

   取 `cmds.session.(projectServersConsenter)` 与 `cmds.selector.(projectServersReviewer)`，缺一就返回；循环最多 3 次：
   `items := c.PendingProjectServers()`，空就返回；`answer, err := reviewer.ReviewProjectServers(items)`，err → `renderer.PrintError(err)` 返回；
   `!answer.Decided` → 返回；`err = c.DecideProjectServers(items, answer)`；`var stale interface{ StaleNames() []string }; if errors.As(err, &stale) { continue }`；
   其他 err → `PrintError` 返回；成功 → 返回。

**Verify**：`go build ./...` → exit 0。

### Step 4：接到启动与切会话

`pkg/tui/run.go`：

1. 删 `projectMCPConsenter` 接口与 `askProjectMCPConsentBeforeSwitch` 函数。
2. `applySlashOutcome` 里原调用改为 `askProjectServers(cmds, renderer)`，旁边注释改为：项目的服务在进入目标会话之前确认，进入时对项目 runner 的对齐会把新允许的启动起来。
3. `Run`：在 `startConversationRuntime(ctx, opts.Session, renderer, state.sessionID)` 之前加 `askProjectServers(cmds, renderer)`，注释：
   TUI 先画出来，再问项目的服务；决定先于项目 runner 的构建，所以允许的服务在它第一次启动时就在。

**Verify**：`go build ./... && go test -tags fts5 ./pkg/tui/` → 除 Step 5 要改的那个测试外全绿（若 `TestNewSessionAsksForProjectMCPConsentBeforeSwitching` 编译失败，属预期，Step 5 改它）。

### Step 5：单测

见 Test plan。

**Verify**：`go test -tags fts5 ./pkg/tui/ ./cmd/forebrain/ ./pkg/process/` → `ok` ×3；`go test ./pkg/architecture/` → `ok`；`gofmt -l pkg/tui cmd/forebrain pkg/process` → 无输出；
包图命令 → 无输出。

### Step 6：Terminal.app 实看外观

按 `run-forebrain` 技能用 Terminal.app（Fira Code 14pt）实际打开一次面板（见 Step 7 的准备），截图确认：组标题强调色、两列对齐、长命令在右列内折行不越界、开关行与列表之间有空行、提示在底部。
有越界或残影就 STOP 5。

### Step 7：真机验收（必须做）

用 `.claude/skills/run-forebrain/mcp_acc.sh` 的做法自建一个临时脚本（放在你的临时目录，不进仓库）：隔离 `FOREBRAIN_HOME`、假模型、
项目目录里放 `.git/` 和 `.forebrain/mcp_servers.yaml`（两条 stdio：`a` → `/bin/sleep 61`、`b` → `/bin/sleep 62`）与 `.forebrain/lsp_servers.yaml`（一条 `fake: {command: /bin/sleep}`）。
首次启动会先出信任页（`Down Enter` 选 Trust）。逐项确认：

1. 启动后**先看到 TUI 欢迎界面**，然后出现面板，标题 `Project servers`、副标题 `3 servers want to run in this project`、`MCP` 与 `Language servers` 两组、全部 `●`、开关 `○`；终端里**没有** `[y/N]`。
2. ↓ 空格（取消 `b`）、Enter → `pgrep -f '/bin/sleep 61'` 有、`pgrep -f '/bin/sleep 62'` 无；`<home>/workspace/state/mcp/project_consent.json` 里 `a` 为 allow、`b` 为 deny；`state/lsp/project_consent.json` 里 `fake` 为 allow。
3. 退出重启 → 不再出现面板。
4. 删掉两个 consent 文件，重启 → 面板再现；Esc → 两个文件里三条全部 deny；`pgrep -f '/bin/sleep 6[12]'` 无；重启不再出现面板。
5. 删掉 consent 文件，重启，光标移到开关行按空格（全部变 `●`）、Enter → `<home>/workspace/state/project_auto_allow.json` 里有这个项目键；
   往 `mcp_servers.yaml` 追加 `c` → `/bin/sleep 63`，重启 → 不出现面板，`pgrep -f '/bin/sleep 63'` 有。
6. 面板打开时按 Ctrl+C → 面板关闭、什么都不记（consent 文件不存在）；进入会话后 `/new` → 面板再现。
7. 任何时刻同一个服务的进程不超过一份（`111` 的不变式仍成立）。

## Test plan

模式参照：`pkg/tui/overlays_test.go` 的 `pickerKeys(t, keys)`（把按键写进管道驱动真 `rawSelector`；常量 `keyUp`、`keyDown`、`keyEnter`；空格就是 `" "`，Esc 是 `"\x1b"`、Ctrl+C 是 `"\x03"`。读键层（`pkg/tui/overlays.go:3565-3577`）对 ESC 后 50ms 内没有后续字节才认成 Esc，
紧跟的非 `[`/`O` 字节会被一起吞掉——所以**Esc 只能放在按键序列的最后**；若这样仍认不出就 STOP 3）；
`pkg/tui/tabbed_picker_test.go:217-232`（`panel(...).layout(40, 0, 0, false).lines` + `stripANSI` 断言画面）；
`pkg/tui/chat_session_test.go` 里的 `consentSession` / `recordingSelector`。

新增 `pkg/tui/project_servers_test.go`：

1. 画面（2 个 MCP + 1 个 LSP，宽 80）：依次含 `Project servers`、`3 servers want to run in this project`、`MCP`、`❯ ● caveman-shrink`、`Language servers`、`● gopls`、
   `○ Always allow this project's servers`、提示原文；1 项时副标题是 `1 server wants to run in this project`；只有 LSP 时不出现 `MCP` 组。
2. 长命令在窄宽度（40）下折到右列内，没有一行超过宽度（参照 `overlays_test.go` 里「width %d cut a row」那类检查）。
3. 状态：`toggle` 两条联动（开关开 ⇒ 全勾；取消一条 ⇒ 开关关）；`move` 夹在两端。
4. 按键（`pickerKeys`）：直接 Enter → 全 allow、`AlwaysAllow` false；空格 + Enter → 第一条 false；↓↓↓（到开关行）+ 空格 + Enter → 全 true 且 `AlwaysAllow` true；
   开关开后 ↑ + 空格 + Enter → `AlwaysAllow` false 且那一条 false；Esc → `Decided` true、全 false；Ctrl+C → `Decided` false。
5. 编排（假会话 + 假 reviewer）：没有待确认项 → reviewer 不被调用；`Decided` false → 不记录；第一次记录返回一个带 `StaleNames()` 的错误 → 再问一次，第二次成功；其他错误 → 不重试。

改 `pkg/tui/chat_session_test.go`：

6. `consentSession` 改实现 `PendingProjectServers` / `DecideProjectServers`（去掉 MCP 专用的两方法）；`TestNewSessionAsksForProjectMCPConsentBeforeSwitching` 改名
   `TestNewSessionAsksForProjectServersBeforeSwitching`，用一个嵌入 `recordingSelector` 并实现 `ReviewProjectServers` 的假 selector：断言面板只出现一次、条目带 `Detail`、
   答案被记录一次、顺序仍是 `decide` 在 `activate` 之前。

`cmd/forebrain`：现有 `TestRunStreamingTerminalKeepsCaretHiddenUntilTheTUI`、`TestRunStreamingTerminalRestoresCaretWhenTrustDeclined` 等必须继续通过（不再桩掉被删的变量）。

## Done criteria

- [ ] `go build ./...` exit 0；`go test -tags fts5 ./pkg/tui/ ./cmd/forebrain/ ./pkg/process/` 全 `ok`，含上述新用例
- [ ] `go test ./pkg/architecture/` `ok`；`gofmt -l pkg/tui cmd/forebrain pkg/process` 无输出；包图只有 `loc` 变化
- [ ] `test ! -f cmd/forebrain/mcp_consent.go`
- [ ] `grep -rn 'askProjectMCPConsentBeforeSwitch\|projectMCPConsenter\|interactiveEnsureProjectMCPConsent\|interactiveEnsureProjectLSPConsent' cmd pkg` 无输出
- [ ] `grep -rn "Allow it to run\|Keep it out" pkg/tui` 无输出
- [ ] Step 6 截图与 Step 7 七项逐一通过（在报告里列出每项的观察结果）

## STOP conditions

1. 前置检查任一行不符（`110` / `111` 没落地或已漂移）。
2. 面板在启动时收不到按键（例如欢迎界面之后读键层还没就绪）——不要改成在 TUI 之前问，停下报告。
3. 读键层不能把单独的 Esc 认出来（需要超时判定之类的改动）。
4. `ResolveConsentProjectContext` 还有别的调用方。
5. Terminal.app 实看有越界、残影或对不齐。
6. 需要给 `pkg/tui` 加新的 `pkg/` import。

## Maintenance notes

- 面板与网页会话里的确认弹窗（`113`）必须保持同一语义：默认全选、Esc 全部不允许、关掉不记、同一个开关。改一边就要改另一边。
- 被 Esc 全部拒绝的条目只能在网页项目页撤回（TUI 没有入口）；若以后要在 `/mcp` 里加撤回，复用 `process.DecideProjectMCPConsentsFor`。
- 面板文案是 owner 选定的，改字前先问 owner（`ui-copy-concise`：不加解释性句子）。
