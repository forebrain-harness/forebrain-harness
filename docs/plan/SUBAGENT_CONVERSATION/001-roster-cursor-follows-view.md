# 计划 001：roster 光标以视图为准，按 agent 键记录

> **执行者须知**：逐步执行。每一步都运行"验证"命令并确认结果后再进入下一步。出现"STOP 条件"里的任何情况，立即停止并报告，不要自行发挥。
> 完成后更新 `docs/plan/SUBAGENT_CONVERSATION/README.md` 里本计划的状态行。**不要提交代码。** 先读 README 的"全局规则"。
>
> **漂移检查（先运行）**：
> `git diff --stat bda9505 -- pkg/tui/run.go pkg/tui/render.go pkg/tui/reducer.go pkg/tui/run_test.go pkg/tui/chat_session_test.go .claude/skills/run-forebrain/driver.sh`
> 基线 `bda9505` 对应干净树（见 README"与并行工作的关系"）：diff 列出文件即有新改动，按函数名和注释原文核对下面"现状"里的摘录；对不上就 STOP。

## 状态

- **优先级**：P1（owner 第 5 点）
- **工作量**：S
- **风险**：LOW（只动 TUI 的 roster 光标与视图切换）
- **依赖**：无
- **类别**：bug
- **基线**：提交 `bda9505`，2026-10-04

## 为什么要做

owner 报告：在主视图点击 subagent 的消息卡片进入它的视图后，agent roster 的箭头仍然指着 `main`。要求定位根因并修复。

根因已经确认：箭头有两个事实来源。roster 没有键盘焦点时，箭头由当前视图推出（这部分是对的，已有测试 `TestRosterCursorFollowsTheViewOpenedByClickingACard` 覆盖）。roster 一旦获得焦点（用户按过一次 ↓），箭头改读 `streamState.agentRosterSelected`。这个下标只被 ↑/↓ 改写，点击卡片、Esc 返回、切换会话、恢复浏览状态都不会改它。复现：主视图下按 ↓（焦点落到 main 行，下标 0），再点击 subagent 卡片；视图已经是该 subagent，箭头仍在 main。

同一个下标还有一个相邻缺陷：它是"第几行"而不是"哪个 agent"。roster 有 `[main, A, B, C]`、光标在 B（下标 2）时 A 结束、行被移除，变成 `[main, B, C]`，下标 2 现在指向 C。此时按 `x` 会取消 C 而不是用户选中的 B。

修复：光标只有一个主人。把它从 `streamState` 的行下标改成渲染器里的 **agent 键**（主视图为 `""`，subagent 为它的 roster key，和 `Renderer.activeView` 同一套键），并让渲染器里**唯一**改变视图的入口同时把光标移到新视图上。

## 现状（2026-10-04 工作区的事实）

### 光标与焦点的状态（`pkg/tui/run.go`）

- `streamState` 字段（约 `:1594-1600`）：

  ```go
  	agentRoster                  AgentRosterSnapshot
  	agentRosterSelected          int
  	// agentRosterFocused is true once the user has moved keyboard focus onto
  	// the roster panel via Down. While false, arrows control input history as
  	// usual and the roster shows no per-row hint; Enter/x on the roster are
  	// inert until focus is entered.
  	agentRosterFocused bool
  ```

- `selectedAgentRosterRow()`（`:1774`）按 `agentRosterSelected` 下标取行，越界时夹到两端。
- `handleOverlayNav(hotkey, activeView)`（`:1792`）：roster 有运行中的 subagent 时，第一次 ↓ 设 `agentRosterFocused = true` 并 `agentRosterSelected = maxInt(0, agentRosterIndexForView(s.agentRoster, activeView))`；之后 ↓ 下标加一并在末尾回绕到 0；↑ 在下标 0 时取消焦点，否则减一。
- `handleAgentRosterLineInput`（`:1841`）：焦点在 roster 时，Enter（空行）调 `renderer.SetActiveView(viewKey)`；`x` 按 `selectedAgentRosterRow()` 取消 subagent 或主运行。
- `refreshAgentRoster`（`:1891`）在行数变少时把下标夹到最后一行。
- `switchStreamSession` 里（`:1728`）`state.agentRosterSelected = 0`、`state.agentRosterFocused = false`。
- `renderComposerWithState`（`:2774`）把 `RosterSelected: state.agentRosterSelected`、`RosterFocused: state.agentRosterFocused` 交给渲染器；`:2793` 用 `selectedAgentRosterRow()` 判断是否点亮"按 x 停止"。

### 箭头的计算（`pkg/tui/render.go`）

- `ComposerRenderState.RosterSelected int`（`:4419`）。
- `agentRosterIndexForView(snapshot, activeView)`（`:5601`）：`activeView == ""` 返回 primary 行，否则返回 `row.ID == activeView` 的行，找不到返回 -1。
- `agentRosterMarkedIndex(snapshot, focused, cursor, activeView)`（`:5626`）：

  ```go
  	if !focused {
  		return agentRosterIndexForView(snapshot, activeView)
  	}
  	if cursor < 0 {
  		return 0
  	}
  	if cursor >= len(snapshot.Rows) {
  		return len(snapshot.Rows) - 1
  	}
  	return cursor
  ```

- `pkg/tui/reducer.go:2926` `buildNormalComposerBlock(..., roster AgentRosterSnapshot, rosterSelected int, ...)`，在 `:2940` 调 `agentRosterMarkedIndex(roster, cs.RosterFocused, rosterSelected, r.activeView)`。

### 改变视图的地方（`pkg/tui/reducer.go`）

- `SetActiveView`（`:4167`）→ `setActiveViewLocked`（`:4181`）：保存旧视图的滚动状态、写 `r.activeView = nextView`、恢复新视图的滚动状态、重绘。
- `openAgentViewAtRowLocked`（`:4216`）：点击卡片时调 `setActiveViewLocked(agentID)`。
- `RestoreBrowseState`（`:4277`）里 `r.activeView = active`（`:4326`）**直接赋值**，不经过 `setActiveViewLocked`。
- `ViewportResetSession` 里 `r.activeView = ""`（`:4981`）**直接赋值**。

### 已有测试

- `pkg/tui/chat_session_test.go` `TestRosterCursorFollowsTheViewOpenedByClickingACard`（约 `:21735`）：只覆盖 roster **未**获得焦点的情况。它构造 `Renderer`、用 `fanoutFrameForTest()` 放一个 fanout 卡片、`rowOfPrimaryText` 找行、`ViewportClickToggle` 点击，再用 `buildComposerBlock` 读出 roster 行。新测试照这个写法。
- `pkg/tui/run_test.go` `TestAgentRosterEnterSwitchesSelectedView`（`:3005`）、`TestAgentRosterXUsesSelectedRowCancel`（`:3037`）、`TestAgentRosterHotkeyStopCancelsSubagent`（`:3057`）、`TestAgentRosterHotkeyStopNoOpWhenNotFocused`（`:3077`）覆盖 roster 的按键。
- 根因复现用的一次性测试（写计划时在仓库外用 `go test -overlay` 跑过，输出 `BUG REPRODUCED: view is "task-b" but the roster cursor is on "❯ main …"`），其主体就是下面第 1 步要加的测试。

## 设计

1. **光标是渲染器的状态，用 agent 键表示。** `Renderer` 新增私有字段 `rosterCursor string`（与 `activeView` 同一套键：主视图 `""`，subagent 为 roster key），放在 `activeView` 字段旁边，并写注释说明"它是 roster 在键盘焦点下指向的 agent；任何视图切换都把它移到新视图上"。注意：`Renderer` 不是 `run.Runner`，不受 `TestRunnerOnlyShrinks` 约束。
2. **只有一个改变视图的入口。** 新增私有方法 `assignActiveViewLocked(view string)`：写 `r.activeView = view` 并写 `r.rosterCursor = view`。`setActiveViewLocked`、`RestoreBrowseState`、`ViewportResetSession` 里对 `r.activeView` 的赋值全部改为调用它。完成后 `grep -n "r.activeView = " pkg/tui/*.go`（排除 `_test.go`）只能命中 `assignActiveViewLocked` 里的那一行。
3. **焦点仍由 `streamState` 持有**（它决定 ↑/↓ 是走历史还是走 roster，`input_events.go` 读的全局原子量 `agentRosterFocused` 也由它写），**下标删除**：
   - 删除 `streamState.agentRosterSelected` 和 `ComposerRenderState.RosterSelected`，以及 `buildNormalComposerBlock` 的 `rosterSelected` 参数。
   - 渲染器新增导出方法：
     - `MoveRosterCursor(snapshot AgentRosterSnapshot, delta int)`：在 `snapshot.Rows` 上按 ↓/↑ 的现有规则移动（↓ 末尾回绕到第一行；↑ 不回绕，调用方在第一行时自己取消焦点，见下）。起点是 `rosterCursorIndex`（见下）。移动后把 `rosterCursor` 设为目标行的视图键（primary 行 → `""`，subagent 行 → `row.ID`）。
     - `RosterCursorRow(snapshot AgentRosterSnapshot) (AgentRosterRow, bool)`：返回光标所在行。
     - 私有 `rosterCursorIndex(snapshot)`：先找 `rosterCursor` 对应的行；找不到（它的行已被移除）就找当前视图的行；还找不到就是 0。
   - `handleOverlayNav` 的 roster 分支：第一次 ↓ 只设焦点（不再写下标：光标此时已经等于当前视图，见第 2 条）；之后的 ↓ 调 `renderer.MoveRosterCursor(s.agentRoster, +1)`；↑ 时若 `RosterCursorRow` 是第一行则取消焦点，否则 `MoveRosterCursor(..., -1)`。为此 `handleOverlayNav` 的第二个参数从 `activeView string` 改为 `renderer *Renderer`（两个调用点 `pkg/tui/run.go:759`、`:3900` 同步改）。
   - `selectedAgentRosterRow()` 删除，调用点（`handleAgentRosterLineInput`、`renderComposerWithState` 的 `rosterStopArmed`、`x`/`ctrl+k k` 相关处）改用 `renderer.RosterCursorRow(s.agentRoster)`。需要给 `handleAgentRosterLineInput` 已有的 `renderer` 参数直接用；`renderComposerWithState` 已有 `renderer`。
   - `refreshAgentRoster` 里的下标夹取删除（键不会因为别的行消失而指错）。
   - `switchStreamSession` 里 `state.agentRosterSelected = 0` 删除（`ViewportResetSession` 把视图和光标一起归零）。
4. **箭头的计算**：`agentRosterMarkedIndex` 改为 `agentRosterMarkedIndex(snapshot, focused, cursorIndex, activeView)`，其中 `cursorIndex` 由 `buildNormalComposerBlock` 用 `r.rosterCursorIndex(roster)` 算出（渲染时持有 `r.mu`，直接读字段）。焦点下返回 `cursorIndex`，否则返回 `agentRosterIndexForView(...)`，其余不变。更新它的注释：说明光标与视图同属渲染器，视图的每一次变化都会把光标带过去，所以焦点下光标与视图只在用户正用 ↑/↓ 浏览 roster 时不同。

## 缓存影响

无。只改 TUI 的光标状态，不涉及任何模型请求。

## 范围

**只改这些文件：**
- `pkg/tui/reducer.go`、`pkg/tui/render.go`、`pkg/tui/run.go`
- `pkg/tui/chat_session_test.go`、`pkg/tui/run_test.go`（加测试、改用到被删字段的旧测试）
- `.claude/skills/run-forebrain/driver.sh`、`.claude/skills/run-forebrain/SKILL.md`（第 5 步加 `click` 命令）

**不要动：**
- `pkg/tui/input_events.go`：它读的 `agentRosterFocused` 原子量语义不变。
- roster 的外观（行文案、提示语、颜色）。
- 任何 `pkg/run`、`pkg/turn`、`pkg/gateway`、`frontend` 文件。网页的 agent 标签页（`AgentViewTabs.vue`）高亮由 `activeAgentView` 直接驱动，没有这个缺陷。

## 步骤

### 第 1 步：先写失败的回归测试

在 `pkg/tui/chat_session_test.go` 紧挨 `TestRosterCursorFollowsTheViewOpenedByClickingACard` 之后加 `TestRosterCursorFollowsAClickedCardWhileTheRosterHasFocus`。照搬那个测试的搭建（`NewRenderer`、`viewportMode = true`、`fanoutFrameForTest()`、`ensurePerAgentVM` 给 `task-a`/`task-b` 各放一条帧、`renderViewport`、三行 roster：`main`(primary)、`task-a`、`task-b`）。然后：

1. 构造 `st := &streamState{agentRoster: roster}`，调用一次 ↓（第 3 步之前用当前签名 `st.handleOverlayNav(hotkeyOverlayDown, r.ActiveView())`，第 3 步改签名后同步改测试）。断言 `st.agentRosterFocused == true`。
2. 渲染 roster（`ComposerRenderState{..., AgentRoster: roster, RosterFocused: true}` 加上当前的选中参数），断言箭头在 `main` 行。
3. `r.ViewportClickToggle(0, rowOfPrimaryText(t, r, "check the sandbox profile"))`，断言 `r.ActiveView() == "task-b"`。
4. 再渲染，断言带 `❯` 的那一行含 `check the sandbox profile`，且全屏只有一个 `❯`。

再加 `TestRosterCursorStaysOnItsAgentWhenAnotherRowLeaves`（放在 `pkg/tui/run_test.go`，靠近 `TestAgentRosterXUsesSelectedRowCancel`）：roster `[main, A, B, C]`，聚焦后 ↓ 两次到 B；把 roster 换成 `[main, B, C]`（模拟 A 结束），调用 `handleAgentRosterLineInput(..., "x")`，用 `TestAgentRosterXUsesSelectedRowCancel` 里的假 session 记录 `CancelSubagent` 收到的 `AgentID`，断言是 B。

**验证**：`CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -run 'TestRosterCursorFollowsAClickedCardWhileTheRosterHasFocus|TestRosterCursorStaysOnItsAgentWhenAnotherRowLeaves' -count=1` → **两条都失败**（第一条报箭头在 main；第二条报取消的是 C）。如果第一条没有失败，STOP：说明根因判断与代码不符。

### 第 2 步：渲染器持有光标，视图只有一个入口

按"设计"第 1、2 条改 `pkg/tui/render.go`（字段）和 `pkg/tui/reducer.go`（`assignActiveViewLocked`，三处赋值改为调用它）。

**验证**：
- `grep -n "r\.activeView = " pkg/tui/*.go | grep -v _test.go` → 只有一行，在 `assignActiveViewLocked` 里。
- `CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -run 'Roster|ActiveView|BrowseState' -count=1` → 除第 1 步的两条新测试外全部通过。

### 第 3 步：删掉下标，按键改为移动渲染器的光标

按"设计"第 3、4 条改 `pkg/tui/run.go`、`pkg/tui/render.go`、`pkg/tui/reducer.go`。修好所有因删除 `agentRosterSelected` / `RosterSelected` 而编译失败的测试：它们原来设置下标的地方，改为调用 `renderer.MoveRosterCursor` 或直接设置视图，断言不变。

**验证**：
- `grep -rn "agentRosterSelected\|RosterSelected" pkg/tui` → 无输出。
- `CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -count=1` → `ok`，第 1 步的两条测试现在通过。

### 第 4 步：补齐其余入口的测试

在 `pkg/tui/run_test.go` 加：

1. `TestRosterEnterKeepsFocusAndCursorOnTheOpenedAgent`：聚焦、↓ 到 B、Enter（`handleAgentRosterLineInput(..., "")`）后，`ActiveView() == B`，焦点仍为 true，`RosterCursorRow` 是 B。
2. `TestEscapeBackToTheConversationMovesTheCursorToThePrimaryRow`：在 B 视图、roster 有焦点时调用 `renderer.SetActiveView("")`（这是 Esc 返回主视图做的事），`RosterCursorRow` 是 primary 行。
3. `TestRestoredBrowseStateCarriesTheCursorToTheRestoredView`：`RestoreBrowseState(RendererBrowseState{ActiveView: "task-b", ...})` 后 `RosterCursorRow` 是 `task-b`。

**验证**：`CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -run 'Roster|ActiveView|BrowseState|Escape' -count=1` → `ok`。

### 第 5 步：真机验证（tmux）

1. 给 `.claude/skills/run-forebrain/driver.sh` 加命令 `click <col> <row>`（1 基坐标）：用 `tmux send-keys -t "$SESSION" -l` 发送 SGR 鼠标按下和抬起两段序列 `ESC[<0;col;rowM`、`ESC[<0;col;rowm`。在 `driver.sh` 的 usage 块（约 `:140-165`）加一行；`SKILL.md` 没有命令表，在它的 Gotchas 一节加一句 `click` 的说明。TUI 启动时已经开启了鼠标上报（`pkg/tui/run.go` 的 `enableMouseSeq`），所以 tmux 原样转发的这两段字节会被读成一次点击。
2. 用 `tool` 模式让主 agent 派发一个会跑一段时间的 subagent（假模型按请求顺序发放脚本里的工具调用，第二个请求就是 subagent 的第一个请求）：

   ```bash
   D=.claude/skills/run-forebrain/driver.sh
   $D reset
   $D start tool '[{"name":"subagent_run","arguments":{"title":"sleepy probe","task":"run the command","subagent_type":"general-purpose"}},{"name":"shell","arguments":{"command":"sleep 40"}}]'
   $D submit 'start the probe'
   $D wait 'sleepy probe' 30
   $D key Down              # roster 获得焦点，箭头在 main
   $D screen                # 记下 subagent 卡片所在的行号和列号
   $D click <col> <row>     # 点卡片
   $D screen
   ```

   如果 `shell` 那一步弹出审批，先在审批浮层里批准（`$D key Enter`），再继续。

**验证**：最后一次 `$D screen` 里，footer 左侧是 `viewing general-purpose · …`，roster 里 `❯` 所在的行是 `general-purpose … sleepy probe`，而不是 `main`。把这两次 `screen` 的输出贴进完成报告。真机做不到（tmux 缺失等）时 STOP 报告，不要跳过。

## 测试计划

新增 5 条测试（第 1 步 2 条、第 4 步 3 条）：

- `TestRosterCursorFollowsAClickedCardWhileTheRosterHasFocus`（本缺陷的回归）
- `TestRosterCursorStaysOnItsAgentWhenAnotherRowLeaves`（相邻缺陷的回归）
- `TestRosterEnterKeepsFocusAndCursorOnTheOpenedAgent`
- `TestEscapeBackToTheConversationMovesTheCursorToThePrimaryRow`
- `TestRestoredBrowseStateCarriesTheCursorToTheRestoredView`

结构参照 `TestRosterCursorFollowsTheViewOpenedByClickingACard` 和 `TestAgentRosterXUsesSelectedRowCancel`。

## 完成标准

- [ ] `CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -count=1` → `ok`
- [ ] `CGO_ENABLED=1 go test -tags fts5 ./pkg/architecture/ -count=1` → `ok`
- [ ] `go vet -tags fts5 ./pkg/tui/` → 退出码 0；`gofmt -l pkg/tui` → 无输出
- [ ] `grep -rn "agentRosterSelected\|RosterSelected" pkg/tui` → 无输出
- [ ] `grep -n "r\.activeView = " pkg/tui/*.go | grep -v _test.go` → 只有 `assignActiveViewLocked` 一行
- [ ] 死代码输出与基线一致
- [ ] 第 5 步的两次屏幕输出已附在报告里，箭头在被点开的 subagent 行上
- [ ] README 状态行已更新

## STOP 条件

- 第 1 步的第一条测试没有失败（根因判断与代码不符）。
- "现状"里的函数或字段已经不存在、或行为与摘录不同（工作区漂移）。
- 删除下标后，发现除"设计"列出的地方以外还有逻辑依赖"第几行"（例如别的面板也读这个下标）。
- 需要改 `pkg/tui/input_events.go` 才能让按键工作。

## 维护说明

- 以后任何新加的"切换视图"路径都必须调用 `SetActiveView` / `setActiveViewLocked`，不得直接写 `r.activeView`；第 2 步的 grep 就是检查点，评审时盯住它。
- 计划 007 会让 composer 草稿也按视图保存与恢复，它同样挂在"视图只有一个入口"上；如果那时需要在视图切换时通知 `streamState`，从 `assignActiveViewLocked` 这一个地方出发，不要在各个调用点各写一遍。
