# 计划 001：roster 行可点击打开对应视图（与卡片任务行同一语义）

> **执行者须知**：逐步执行。每一步都运行"验证"命令并确认结果后再进入下一步。出现"STOP 条件"里的任何情况，立即停止并报告，不要自行发挥。
> 完成后更新本目录 `README.md` 里本计划的状态行。**不要提交代码**（owner 手动审阅提交，仓库铁律）。先读本目录 `README.md` 的"全局规则"。

> **漂移检查（先运行）**：本计划写于 2026-10-07，基线是当时的工作区（HEAD `936df40` + SUBAGENT_CONVERSATION 全套未提交改动，不可用 git diff 干净核对）。因此摘录一律**按函数名和注释原文**核对；对不上就 STOP。

## 状态

- **优先级**：P1（owner 2026-10-07 指令："roster 行点击不开视图（卡片才行）……必须定位根因并彻底修复"）
- **工作量**：S
- **风险**：LOW（只加命中测试与一条打开路径，不动现有几何）
- **依赖**：无
- **类别**：bug
- **基线**：工作区（2026-10-07），HEAD `936df40`

## 为什么要做

agent roster 的键盘路径（↓ 聚焦、Enter 打开）早已存在，鼠标却只能通过点 subagent 卡片的任务行进入视图。roster 行画着 `Enter to view` 的提示，用户点击它却毫无反应——同一功能两套输入，鼠标那套是坏的。009 真机验收时顺带发现，owner 裁定必须修。

## 根因（已定位，2026-10-07 核实）

鼠标点击全部走 `Renderer.ViewportClickToggle(col, row)`（两处分发：`pkg/tui/run.go:718` 与 `:1348`）。该函数在 `pkg/tui/reducer.go:5381-5383`：

```go
	if row < 0 || row >= r.vpHeight {
		return false
	}
```

而 roster **画在 composer 区**（视口主体之下）：`buildNormalComposerBlock`（`pkg/tui/reducer.go:3468-3472`）把 roster 行追加在 footer 之后：

```go
	if rosterVisible {
		lines = append(lines, "")
		lines = append(lines, rosterLines...)
	}
```

composer 区的鼠标命中测试只有一处——`composerCaretAtLocked`（`pkg/tui/reducer.go:6016-6024`）委托给 `composerTextArea.caretAt`，其注释明确非 composer 文本的行（边框、footer、状态行）返回 false，roster 行自然也 miss。结论：**roster 行是纯文字，没有任何命中测试，也没有任何点击分发指向它**。

## 设计

点击 roster 行 = 键盘 Enter 的等价物：打开该行的视图（subagent 行 → 它的视图；primary 行 → 回主视图），复用 001（SUBAGENT_CONVERSATION）定下的唯一视图入口。

1. **composerBlock 记录 roster 的几何。** `pkg/tui/reducer.go:3208` 的 `composerBlock` 增加字段（放 `text composerTextArea` 旁）：

   ```go
   	// roster records where the agent roster's rows sit in this block's
   	// full line space, and each row's view key ("" for the primary row) —
   	// the same key Enter opens — so a mouse click on a roster row can open
   	// that agent's view the way a click on a card's task row does.
   	rosterFirstLine int
   	rosterKeys      []string
   ```

   `buildNormalComposerBlock` 在追加 roster 行时填写：`rosterFirstLine = len(lines)`（追加空白行**之后**、追加 rosterLines **之前**），`rosterKeys[i] = agentRosterRowViewKey(roster.Rows[i])`（`pkg/tui/render.go:5606`，已存在：subagent 行 → `row.ID`，primary 行 → `""`）。roster 不可见时保持零值。
2. **渲染器命中测试。** `pkg/tui/reducer.go` 新增私有方法，几何算法照抄 `composerCaretAtLocked`（`:6016`）：

   ```go
   // composerRosterViewAtLocked resolves a click at 0-based screen coordinates
   // to the view key of the roster row painted there, if any. Caller holds r.mu.
   func (r *Renderer) composerRosterViewAtLocked(col, row int) (string, bool)
   ```

   守卫与 `composerCaretAtLocked` 相同（`overlayActive || composerSuppressed` → miss；`row < r.vpBodyHeight` → miss）；块内行号 `= row - r.vpBodyHeight + r.vpLastComposer.scrollOffset`；落在 `[rosterFirstLine, rosterFirstLine+len(rosterKeys))` 内返回 `(rosterKeys[行号-rosterFirstLine], true)`——**primary 行也返回 `("", true)`**（空键就是主视图，与 Enter 相同）。`_ = col`（整行都是目标，与卡片任务行一致）。
3. **接进 ViewportClickToggle。** 在 `:5384` 的 jump-to-bottom 判断之后、`:5381` 的边界拒绝**之前**插入：

   ```go
   	if key, ok := r.composerRosterViewAtLocked(col, row); ok {
   		r.setActiveViewLocked(key)
   		r.paintViewportLocked()
   		return true
   	}
   ```

   `composerRosterViewAtLocked` 自带 `row >= vpBodyHeight` 的判定，视口内的行照旧走原路径，边界拒绝对非 roster 行保持不变。执行者核对 `SetActiveView`（`pkg/tui/reducer.go:4644`）内部就是锁 + `setActiveViewLocked`——此处已持锁，必须直接调 `setActiveViewLocked`，不要调导出方法造成死锁。空键 `""` 经 `setActiveViewLocked` 归一到主视图（与 Esc/Enter 相同）。
4. **悬停高亮不加**（本轮最小改动；视口行的悬停机制属另一套，不扩展）。

## 缓存影响

无。纯 TUI 鼠标命中与视图切换，不涉及任何模型请求。

## 范围

**只改**：`pkg/tui/reducer.go`、`pkg/tui/run_test.go` 或 `pkg/tui/chat_session_test.go`（按现有 roster 测试所在文件放）。
**不要动**：`ViewportClickToggle` 对视口行的既有语义（卡片任务行、折叠展开、jump-to-bottom）；`formatAgentRosterLines` 的行文案；`pkg/tui/input_events.go`；任何 `pkg/run`、`pkg/gateway`、`frontend`。

## 步骤

### 第 1 步：先写失败的测试

在 `pkg/tui/chat_session_test.go` 紧挨 `TestRosterCursorFollowsAClickedCardWhileTheRosterHasFocus` 加 `TestRosterRowClickOpensItsView`：照搬该测试的搭建（`NewRenderer`、`viewportMode=true`、`fanoutFrameForTest()`、`ensurePerAgentVM` 两份帧、三行 roster：main/task-a/task-b、`renderViewport`）。然后用 `buildComposerBlock`（或现有读取 composer 的辅助）找到 roster 各行在**屏幕坐标**里的行号（= `vpBodyHeight` + 块内行号，scrollOffset 为 0），依次：

1. `r.ViewportClickToggle(2, taskB行)` → 断言 `r.ActiveView() == "task-b"`。
2. `r.ViewportClickToggle(2, main行)` → 断言 `r.ActiveView() == ""`。
3. `r.ViewportClickToggle(2, footer行)` → 返回 false 且视图不变。

**验证**：`CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -run TestRosterRowClickOpensItsView -count=1` → **失败**（第 1 条断言：视图仍是 ""）。不失败就 STOP（根因判断与代码不符）。

### 第 2 步：实现（设计第 1–3 条）

**验证**：第 1 步测试转绿；`CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -count=1` → `ok`（尤其 `TestRosterCursorFollowsAClickedCardWhileTheRosterHasFocus`、`TestRosterEnterKeepsFocusAndCursorOnTheOpenedAgent`、`TestViewportComposerRendersRosterBelowFooterOnlyForRunningSubagent` 不许改断言）。

### 第 3 步：真机（tmux 假模型）

用 010 的脚本模式（driver.sh，`ENABLE_SUBAGENT=1`，`subagent_fanout` 两个任务带 `max_parallel:2`，后接 `shell sleep 40`），卡片出现后 `$D screen` 找 roster 行的屏幕坐标，`$D click <col> <row>` 点 subagent 行 → footer 变 `viewing general-purpose · …`；再点 roster 的 main 行 → 回主视图。贴两次 screen。

**验证**：两次屏幕输出附进报告。

## 完成标准

- [ ] `CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -count=1` → `ok`；全量 `./... -count=1` → 全 `ok`
- [ ] `gofmt -l pkg cmd` 无输出；`go vet -tags fts5 ./...` 退出码 0；架构测试 `ok`
- [ ] 死代码与 `/tmp/deadcode-before.txt` 基线一致（按符号名）
- [ ] 第 3 步两次屏幕输出在报告里
- [ ] README 状态行已更新

## STOP 条件

- 第 1 步的测试没有失败。
- `setActiveViewLocked` 与 `SetActiveView` 的锁关系与设计第 3 条描述不符（例如内部还做了别的需要外层配合的事）。
- roster 行不在 `vpLastComposer.lines` 里（说明本计划的几何假设错了）。

## 维护说明

- 以后任何"画在 composer 区的可点元素"都走 `composerRosterViewAtLocked` 这条路：块内记录几何 + 渲染器命中测试 + `ViewportClickToggle` 分发。评审时看到 composer 区新文字元素不可点，问一句为什么。
