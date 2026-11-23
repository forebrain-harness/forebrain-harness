# 计划：主/子代理视图的 Working 与 Worked-for 行彻底解耦

> **执行者指令**：逐 step 执行；每个 step 的验证命令必须通过才进入下一步。
> 命中「STOP conditions」任一条就停下报告，不得即兴改方案。
> 本计划自包含：你没有本会话的任何上下文，所需事实全部内联。
>
> **Drift check（最先跑）**：基线 `aace268f`（dev，写计划时工作区 clean）。
> `git diff --stat aace268f..HEAD -- pkg/tui/run.go pkg/tui/render.go pkg/tui/reducer.go pkg/tui/commands.go`
> 有改动时，把下方「Current state」摘录与现场代码逐段对照；**内容不符即 STOP**（仅行号漂移不算）。
> 动手前 `peer_list` 确认没有其他会话正在改 pkg/tui（写计划时 forebrain-harness-68 在改
> pkg/tool 的 edit_file、forebrain-harness-93 的 WIP 在 .worktrees/exec-316-middle-dot，均不碰 pkg/tui）。

## Status

- **Priority**: P1（owner 报障，交互语义红线）
- **Effort**: M
- **Risk**: MED（改的是所有瞬态状态行共用的选择/重绘机制）
- **Depends on**: none
- **Category**: bug
- **Planned at**: commit `aace268f`, 2026-11-17
- **落盘约定**：本文件是会话计划文件。批准后执行者先把它复制到
  `docs/plan/SUBAGENT_WORKING_LINE_DECOUPLING_PLAN.md`（本仓惯例目录，已 gitignore，**禁止 commit**），
  后续回填验收记录写在该副本。

## Why this matters（目标）

owner 截图报障：主代理 turn 结束（主视图画出 `— Worked for 1m 23s · … —`）时，仍在执行的
子代理在自己的视图里**丢失了 `Working (…)` 行**。owner 要求：主视图与子代理视图的
working 行、worked-for 行必须**解耦、独立**——主代理结束不得影响子代理视图的两类行。
本仓铁律：主视图、子代理视图、回放（/resume）三处同类行为必须一致；只修根因，不修症状。

调查确认两条独立根因链（均只读源码证实；**计划模式禁止执行复现命令**，复现是 Step 1，
执行者必须先复现失败再动手）：

- **链 A（live）**：working 行是单一瞬态源，formatter 虽按视图分流，但**源的存活期只绑主
  turn**；主 run 一结束源被删，子代理视图的行随之消失。子代理自己的 worked-for 行是事件驱动
  的独立帧，live 路径本就独立（Step 1 真机一并确认）。
- **链 B（resume/会话切换）**：replay 用一次性 tracker，live tracker/roster 不收养仍在运行
  的子代理窗口 → /resume 切回后子视图既无 working 行，子代理结束时也画不出 worked-for 行。

## Current state（根因证据链，全部来自顾问自己的读取）

文件角色：
- `pkg/tui/run.go` — TUI 主循环 `Run`；turn 驱动 `runTurnWithDispatch`；通知处理闭包
  `processUINotification`；working 行 formatter `workingStatusFormatter`。
- `pkg/tui/render.go` — `Renderer`；瞬态状态源注册/选择/结束（`transientSource*`）。
- `pkg/tui/reducer.go` — 事件归约 `Reducer.Reduce`；`Tracker`（含每子代理 `agentRuns`）；
  视图切换 `setActiveViewLocked`；replay 卡片收养 `adoptReplayedCards`。
- `pkg/tui/commands.go` — `commandController`；`replaySession`（/resume、/rewind 共用）。

### 链 A：单一 working 源，存活期绑主 turn

1) turn 开始安装唯一源（`run.go:4084`）：
```go
stopStatus := renderer.StartTimedTransientStatus(transientSourceWorking, workingStatusFormatter(tracker.SnapshotActiveRun, tracker.SnapshotAgentRun))
```

2) formatter 按视图分流（`run.go:4815-4838`，节选）——子代理分支数据是独立的：
```go
func workingStatusFormatter(counterSnapshot func() Counters, agentSnapshot func(string) (AgentRunSnapshot, bool)) transientStatusFormatter {
	return func(elapsed time.Duration, tick int, view string) string {
		if view = strings.TrimSpace(view); view != "" {
			if agentSnapshot == nil { return "" }
			snap, ok := agentSnapshot(view)
			if !ok || snap.Ended || snap.Paused { return "" }
			if snap.Counters.Retry.Active() { return transportRetryStatusLine(tick, snap.Counters.Retry, time.Now()) }
			title := transientSpinnerFrame(tick) + " Working (" + formatWorkingElapsed(snap.Elapsed) + ")"
			// … counters / firstToken 段 …
			return title
		}
		// … 主视图分支：Working (elapsed • esc to interrupt) …
	}
}
```

3) 主 run 结束 → reducer 产 `WorkedStatus`（`reducer.go:1436` `worked := r.formatWorkedStatusTitle(workDuration, runID)`，
`reducer.go:1466` `return EventResult{Frames: out, WorkedStatus: worked}`）→ 通知处理器
（`run.go:410-419`）**无条件结束 working 源**：
```go
if ev.WorkedStatus != "" {
	turnWorkedStatus = ev.WorkedStatus
	renderer.FinishTransientStatus(transientSourceWorking)   // ← 子代理视图的行在这里被连坐删除
	renderer.RenderFrame(Frame{Kind: FrameStatus, Title: ev.WorkedStatus, Final: true})
	// … RefreshComposerTokenUsage …
}
```
turn 函数返回时 defer 再删一次（`run.go:4112-4121`，LIFO：先 `stopStatusOnce()` 后
`FinishTransientStatus(transientSourceWorking)`）；withdraw 路径同样删（`run.go:3383`）。

4) 源被删后选择器对所有视图返回空（`render.go:1039-1047`）：
```go
func (r *Renderer) transientStatusTextLocked() string {
	if !r.transientActive { return "" }
	if render := r.transientRenderLocked(); render != nil { return render(r.activeView) }
	return ""
}
```
`transientRenderLocked`（`render.go:1051-1065`）按 `transientSourcePriority`
（`render.go:1003-1007`：mcp、lsp、working）取**第一个存活源**，不看渲染结果是否为空。

5) 子代理自己的计时数据是 session 级、与主 run 无关（`reducer.go:8452-8458` 注释 +
`agentRun`/`AgentRunSnapshot` `reducer.go:8471-8494`；`SnapshotAgentRun` `reducer.go:8731-8751`：
`run == nil || run.startedAt.IsZero()` 才 !ok；`ended = !run.endedAt.IsZero()`）。
**数据还在，行的载体没了**——这就是根因，不是数据问题。

6) 视图切换不重装任何源（`reducer.go:5657` `setActiveViewLocked` 只换视图+重绘）；idle 期间
没有任何安装点。同族缺口：`SubagentPausedMsg`→`ObserveAgentPaused(true)`（`reducer.go:1661`）、
`SubagentInputDeliveredMsg`→`ObserveAgentPaused(false)`（`reducer.go:1640`）——源已删时
resume 也不会重装。

### 子代理 worked-for 行（live 已独立，勿改坏）

`SubagentEndedMsg` → `reducer.go:1750-1758`：`ObserveAgentEnded` 冻钟 +
`closing := r.subagentClosingFrames(agentID, m)` + `closing = append(closing, r.subagentWorkedFrames(agentID)...)`。
`subagentWorkedFrames`（`reducer.go:2338-2371`）从 `SnapshotAgentRun` 读时长/计数/first-token/
结束时刻，产 `Frame{Kind: FrameStatus, Title: "Worked for …", AgentID: agentID, Final: true}`，
按 AgentID 路由进子代理自己的 VM。**事件驱动，与主 turn 状态无关。**

### 链 B：replay 不收养 running 子代理的 tracker/roster

`commands.go:152-167`（节选，注释是原文）：
```go
// The replay's reducer carries a tracker so a replayed subagent view
// closes the way live closed it … The tracker is this throwaway reducer's own:
// the cards built here are adopted by the live reducer below, the tracker is not.
reducer := (&Reducer{}).WithTracker(NewTracker())
replayTimelineWithWorkers(c.renderer, turns, events, reducer, planUpdates, workerTranscripts)
flushReplayReducer(c.renderer, reducer)
if c.replayCardsTo != nil {
	c.replayCardsTo.adoptReplayedCards(reducer)
}
```
`adoptReplayedCards`（`reducer.go:638-643`）只收养
`fanoutStates / cardTaskOfAgent / queryRowsOfExecution / endedExecutions`。
live tracker 的 `ObserveAgent` 唯一喂点是 live `SubagentSpawnedMsg`（`reducer.go:1668`）；
roster（`agentRoster`/`agentRosterOrder`，`reducer.go:1916-1946`）同样不收养。
`switchStreamSession`（`run.go:~2066-2080`）先 `tracker.Reset()` 再
`renderer.FinishAllTransientStatus()`；随后 `printSessionResumeContext`→`replaySession`
（调用顺序见 `run.go:5499-5501`：先 switch 后 replay，收养发生在 Reset 之后，无覆盖问题）。
→ /resume 切回仍有 running 子代理的会话：`SnapshotAgentRun` !ok → 无 working 行；
子代理之后结束时 `ObserveAgentEnded` 建 `startedAt=0` 的窗口 → 仍 !ok →
`subagentWorkedFrames` 返回 nil → **无 worked-for 行**；roster 也无行。
replay 映射含 paused/input-delivered（`commands.go:1544-1559`），一次性 tracker 的状态是完整的。

### 通知处理器与接线点（改动落点）

`run.go:244-252`：`tracker := NewTracker()`；`var reducer Reducer; reducer.WithTracker(tracker)`；
`cmds.replayCardsTo = &reducer`。
`run.go:281` `processUINotification := func(m any) {`；`run.go:340` `ev := reducer.Reduce(m)`；
`run.go:361-377` 类型 switch 现有 case：`TokenUsageDeltaMsg` / `RunEndedMsg` /
`SubagentSpawnedMsg` / `SubagentEndedMsg`（后两者 `state.refreshAgentRoster(&reducer)`）。
`streamState` 定义 `run.go:1748`；`commandController` 定义 `commands.go:25`（已有
`resetScreen func()`、`replayCardsTo *Reducer` 等主循环接线字段，新字段照此模式）。
`StartTimedTransientStatus`（`render.go:1116-1145`）：200ms ticker goroutine，每 tick
`installTransientStatus(source, closure)`，closure 在**绘制时**按 `r.activeView` 求值
（视图切换立即生效，既有测试 `TestWorkingLineIsRenderedForTheViewOnScreen` 钉住此语义）。
`finishTransientLocked`（`render.go:1162-1180`）用 `transientStatus` 镜像字段的 nil 变化决定重绘；
该字段仅此处读（`render.go:225-232` 定义）。

### 本仓约定（必须遵守）

- 注释风格：解释「为什么」的长注释，见 `reducer.go:8452-8458`、`commands.go:152-157` 原文。
- 测试文件必须与生产文件同名对应（`pkg/architecture` `TestTestFilesCorrespondToProductionFiles`）：
  新测试写进既有 `run_test.go` / `reducer_test.go` / `chat_session_test.go` / `commands_test.go`，
  **不新建测试文件**。生产文件数上限 34（`maxProductionFilesPerPackage`，cache_test.go:1532），
  pkg/tui 现 32 个——**本计划不新增生产文件**，全部改在既有四个文件里。
- 瞬态行测试模式：`NewRenderer(nil, nil)` + `r.setActiveViewLocked(agentID)` +
  `stripANSI(strings.Join(r.buildComposerBlock(ComposerRenderState{Cursor: &cursor}, 120).lines, "\n"))`
  （ exemplar：`chat_session_test.go:23667-23705` `TestWorkingLineIsRenderedForTheViewOnScreen`）。
- reducer 直驱模式：`var red Reducer; red.WithTracker(tracker); red.Reduce(SubagentSpawnedMsg{…})`
  （exemplar：`chat_session_test.go:23712+` `TestSubagentViewIsClosedByItsOwnWorkedLine`）。
- 子代理视图 read-only composer 也画 statusText（`reducer.go:4003-4020`
  `buildReadOnlyComposerBlock`）——行在两种 composer 形态都必须出现。
- owner 规则：验收一律 fake 模型；不 commit；改动留工作区。

## Commands you will need

```bash
export PKG_CONFIG_PATH="$PWD/third_party/pdfium/$(go env GOOS)_$(go env GOARCH)"
```

| 用途 | 命令 | 预期 |
|---|---|---|
| 编译 | `CGO_ENABLED=1 go build -tags fts5 ./...` | exit 0 |
| 定点测试 | `CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -run '<用例正则>' -count=1` | PASS |
| 包级回归 | `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/... ./cmd/forebrain -count=1` | 全 PASS |
| 架构门禁 | `CGO_ENABLED=1 go test -tags fts5 ./pkg/architecture -count=1` | PASS |
| vet | `CGO_ENABLED=1 go vet -tags fts5 ./...` | exit 0 |
| 格式 | `gofmt -l cmd pkg third_party plugins` | 无输出 |
| 真机 | `.claude/skills/run-forebrain/driver.sh`（见 Step 1/7 配方） | 逐屏取证 |

漏 `-tags fts5` 的失败模式是 `no such module: fts5`——工具链错误，不是产品 bug。

## Scope

**In scope（只许改这些）**：
- `pkg/tui/run.go` — formatter 拆分、`agentWorkingLine` 协调器、通知/切换接线、4084 调用点。
- `pkg/tui/render.go` — 新源常量与优先级、选择器 fall-through、`finishTransientLocked`
  重绘判定、`StartTimedTransientStatus` 空文本跳绘。
- `pkg/tui/reducer.go` — `Tracker.HasActiveAgentRun`、`Reducer.adoptReplayedRunningAgents`。
- `pkg/tui/commands.go` — `afterReplay` 接线字段、收养调用。
- `pkg/tui/tty_session.go` — 删除随链 C 镜像删除而死掉的 `endsOnError`（追加范围，见下）。
- 对应既有测试文件：`pkg/tui/run_test.go`、`pkg/tui/reducer_test.go`、
  `pkg/tui/chat_session_test.go`、`pkg/tui/commands_test.go`（更新断言+新增用例）。
- `docs/plan/SUBAGENT_WORKING_LINE_DECOUPLING_PLAN.md`（本计划副本，gitignore，不 commit）。

**Out of scope（看起来相关但不碰）**：
- `subagentWorkedFrames` / `subagentClosingFrames` / 主 `WorkedStatus` 生成逻辑——已独立且正确，
  只允许被测试钉住，不允许改行为。
- `pkg/run`、`pkg/turn`、`pkg/event`、gateway/web 前端——引擎事件与 web 抽屉不经过 TUI 瞬态源；
  本 bug 是 TUI 投影层缺陷。若 Step 1 真机发现 web 抽屉同症状，**STOP 报告**，另行立项。
- `.claude/skills/run-forebrain/fake_provider.py` 仓库本体——复现用 $TMPDIR scratch 拷贝加旋钮，
  不改仓库技能文件。
- peer 的 WIP（pkg/tool edit_file、.worktrees/exec-316-middle-dot）——不碰、不 revert、不 commit。
- 不新增生产文件/测试文件（架构门禁）。

## Git workflow

owner 常设裁决：**直接在当前源码实施，不用 worktree/快照；禁止任何 git commit**；
改动留工作区由 owner 审阅。计划文件禁止 commit。

## 修复设计

### 行为变化矩阵（旧 → 新；★ 为刻意修订）

| 场景 | 旧 | 新 |
|---|---|---|
| 主 turn 运行中，主视图 | Working(turn 时钟) | 不变 |
| 主 turn 运行中，子视图（子 running） | 子自己的 Working | 不变 |
| ★ 主已结束，子 running，子视图 | **无行（bug A）** | 子自己的 Working |
| 主已结束，子 running，主视图 | 无行 | 不变（无行） |
| 子 ended，子视图 | Worked-for 帧 | 不变 |
| ★ 子 paused 后 resume，主 idle | **无 Working（bug A 同族）** | Working |
| 子 paused，子视图 | 无 Working（paused 帧） | 不变 |
| MCP/LSP 启动行 | 压过 working | 不变（压过两个源） |
| withdraw 主 turn | working 删除 | 不变（withdraw 窗口在首 token 前，尚无子代理） |
| 会话切换 | 全部行清空 | 不变 + agent ticker 停止 |
| ★ /resume 切回有 running 子的会话 | **无 Working、无 roster 行、子结束无 Worked-for（bug B）** | 三者恢复 |
| /rewind 同会话 | live tracker 为准 | 不变（收养跳过 live 已知窗口） |
| replay 已结束子代理 | Worked-for 来自一次性 tracker | 不变 |

### A. 两个独立源（run.go + render.go）

1. `render.go`：新增 `transientSourceAgentWorking = "agent-working"`，加入
   `transientSourcePriority`（mcp、lsp、working、agent-working）。
2. **选择器 fall-through-on-empty**（使能改动，必须有）：`transientStatusTextLocked` 改为
   按优先级取第一个对 `r.activeView` 渲染**非空**的源；未知源兜底循环同样跳过空文本。
   原因：主源存活时其对子视图渲染 ""，若不 fall-through 会遮住 agent 源。
   `transientRenderLocked` 删除；`syncTransientLocked` 改为
   `transientActive = 任一非 nil 源存活`；删除 `transientStatus` 镜像字段
   （仅 `finishTransientLocked` 读它）。
3. `finishTransientLocked` 重绘判定改为：删除前后各调一次 `transientStatusTextLocked()`，
   `viewportMode` 下文本变化才 `paintViewportLocked()`。
4. `workingStatusFormatter` 拆成两个（run.go），渲染逻辑逐字搬移、不改文案：
   - `workingStatusFormatter(counterSnapshot func() Counters)`：`view != ""` 直接返回 ""；
     其余即现主分支。
   - `agentWorkingStatusFormatter(agentSnapshot func(string) (AgentRunSnapshot, bool))`：
     `view == ""` 或 nil 返回 ""；其余即现子分支（retry/时钟/counters/firstToken）。
5. **空文本跳绘（性能护栏，必须有）**：`StartTimedTransientStatus` 的 ticker paint 闭包内，
   先短暂持锁读 `r.activeView`，求值 `text := formatter(since, tick, view)`；
   若 `text == lastText && 已安装` 则跳过本次 install（源仍在 map 里）；否则
   `lastText = text` 并照旧 install **按视图求值的 closure**（保持视图切换立即生效语义）。
   理由：主 idle + 后台子代理运行时，agent 源对主视图恒渲染 ""，不得引入 5Hz 全视口重绘
   （owner 有长会话性能红线）。MCP/connect 源同样受益，行为不变。
6. 协调器（run.go，不新增文件）：
```go
// agentWorkingLine owns the subagent views' live Working line. Its lifetime is
// the subagents' own executions — never the conversation's turn.
type agentWorkingLine struct{ stop func(string) }

func (l *agentWorkingLine) sync(renderer *Renderer, tracker *Tracker) {
	if renderer == nil || tracker == nil { return }
	if tracker.HasActiveAgentRun() {
		if l.stop == nil {
			l.stop = renderer.StartTimedTransientStatus(transientSourceAgentWorking,
				agentWorkingStatusFormatter(tracker.SnapshotAgentRun))
		}
		return
	}
	if l.stop != nil { l.stop(""); l.stop = nil }
	renderer.FinishTransientStatus(transientSourceAgentWorking)
}
```
   `streamState`（run.go:1748）加字段 `agentWorking agentWorkingLine`。
   `sync` 调用点：
   - `processUINotification`：`SubagentSpawnedMsg`/`SubagentEndedMsg` 既有 case 各加一行；
     新增 `case SubagentPausedMsg, SubagentInputDeliveredMsg:` 调 sync（归约之后）。
   - `switchStreamSession`：`FinishAllTransientStatus()` 之后调
     `state.agentWorking.sync(renderer, tracker)`（tracker 刚 Reset → 停 handle）。
   - replay 之后：`commandController` 加接线字段 `afterReplay func()`（照 `resetScreen`
     模式），Run 在 `cmds.replayCardsTo = &reducer`（run.go:252）旁设置
     `cmds.afterReplay = func() { state.agentWorking.sync(renderer, tracker) }`；
     `replaySession` 末尾（EndBatch 之后）调用。覆盖 /resume、/rewind、switch 后 replay 全部调用点。
   - `runTurnWithDispatch` 4084 改传 `workingStatusFormatter(tracker.SnapshotActiveRun)`。
   进程退出时 goroutine 随进程结束（与既有源一致），不另加 shutdown 钩子。

### B. replay 收养 running 子代理窗口（reducer.go + commands.go）

1. `Tracker.HasActiveAgentRun() bool`：RLock 扫 `agentRuns`，
   `!startedAt.IsZero() && endedAt.IsZero() && !paused` 任一即 true（与 formatter 的
   ended/paused 判据同一语义，session 级存在性判断）。
2. `Reducer.adoptReplayedRunningAgents(from *Reducer)`：按 `from.agentRosterOrder` 遍历
   `from.agentRoster` 行；取 `from.tracker.agentRuns[id]`，仅收养
   `startedAt` 非零且 `endedAt` 为零的窗口（running+paused 都收，paused 标志随窗口带过来）；
   **live tracker 已有 startedAt 非零窗口的 id 跳过**（/rewind 时 live 为准）；
   拷贝整个 `agentRun`（counters/executionID/startedAt/paused），并
   `r.upsertSubagentRoster(id, row.Label, row.Title, row.Task, row.Status)`（幂等，顺序随遍历）。
   注释写明：replay 的一次性 tracker 拥有完整生命周期（含 paused/input-delivered 映射），
   收养只补 live 缺失的窗口，不覆盖 live。
3. `commands.go:164-166` 收养块追加 `c.replayCardsTo.adoptReplayedRunningAgents(reducer)`。

## Steps

### Step 0：落盘计划副本 + 基线核对
复制本文件到 `docs/plan/SUBAGENT_WORKING_LINE_DECOUPLING_PLAN.md`；跑 drift check 与 `peer_list`。
**Verify**：`git rev-parse --short HEAD` 记录当前 SHA；摘录对照无内容不符。

### Step 1：复现（改任何代码之前；两条都必须失败）

**1a 单元级（go overlay，不写仓库）**。顾问已在会话计划目录备好
`repro_working_line_test.go` 与 `overlay.json`（路径见本文件所在目录；若不可用，按下方源码
自建到 $TMPDIR）。测试驱动 reducer+renderer 走 Run 的真实调用序
（4084 安装 → 410-419 handler → defer LIFO stop+Finish）：

```go
package tui

import (
	"strings"
	"testing"
	"time"
)

func TestReproSubagentWorkingLineSurvivesMainRunEnd(t *testing.T) {
	const agentID = "subagent-repro"
	tracker := NewTracker()
	var red Reducer
	red.WithTracker(tracker)
	r := NewRenderer(nil, nil)
	stop := r.StartTimedTransientStatus(transientSourceWorking,
		workingStatusFormatter(tracker.SnapshotActiveRun, tracker.SnapshotAgentRun))
	// ticker goroutine 的首次 install 是异步的，等源存活再断言 precondition。
	deadline := time.Now().Add(2 * time.Second)
	for !r.TransientStatusSourceLive(transientSourceWorking) && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	red.Reduce(RunStartedMsg{RunID: "run-1"})
	red.Reduce(SubagentSpawnedMsg{AgentID: agentID, AgentType: "general-purpose", TaskID: agentID, Task: "probe", Timestamp: time.Now()})
	r.setActiveViewLocked(agentID)
	painted := func() string {
		cursor := 0
		return stripANSI(strings.Join(r.buildComposerBlock(ComposerRenderState{Cursor: &cursor}, 120).lines, "\n"))
	}
	if got := painted(); !strings.Contains(got, "Working (") {
		t.Fatalf("precondition failed: no working line while main runs:\n%s", got)
	}
	ev := red.Reduce(RunEndedMsg{RunID: "run-1", ClosesTurn: true})
	if ev.WorkedStatus == "" { t.Fatal("no WorkedStatus") }
	r.FinishTransientStatus(transientSourceWorking)
	r.RenderFrame(Frame{Kind: FrameStatus, Title: ev.WorkedStatus, Final: true})
	stop("") // defer LIFO: stopStatusOnce before FinishTransientStatus
	r.FinishTransientStatus(transientSourceWorking)
	if snap, ok := tracker.SnapshotAgentRun(agentID); !ok || snap.Ended {
		t.Fatalf("subagent should still be running: ok=%t %+v", ok, snap)
	}
	if got := painted(); !strings.Contains(got, "Working (") {
		t.Fatalf("BUG REPRODUCED: subagent view lost its Working line after main ended:\n%s", got)
	}
}
```
overlay.json：`{"Replace":{"<repo绝对路径>/pkg/tui/zz_repro_working_line_test.go":"<测试文件绝对路径>"}}`。
**Verify（预期 FAIL）**：
`CGO_ENABLED=1 go test -tags fts5 -overlay <overlay.json> ./pkg/tui -run TestReproSubagentWorkingLineSurvivesMainRunEnd -count=1`
→ 失败信息含 `BUG REPRODUCED`。若 PASS 或 precondition 失败 → **STOP**（根因链与现场不符）。

**1b 真机（fake provider，tmux）**。harness 的 `subagent-steer` 模式硬编码 `subagent_run`
（主代理会等待子代理，不合场景），用 scratch 拷贝加 `subagent_send` 旋钮：

```bash
W="${TMPDIR:-/tmp}/forebrain-run-wl"; mkdir -p "$W"
python3 - <<'PY'
import os, pathlib
src = pathlib.Path(".claude/skills/run-forebrain/fake_provider.py").read_text()
old = '"function": {"name": "subagent_run", "arguments": SUBAGENT_NET_PROBE_ARGS},'
new = '"function": {"name": ("subagent_send" if os.environ.get("FAKE_SUBAGENT_DISPATCH") == "send" else "subagent_run"), "arguments": SUBAGENT_NET_PROBE_ARGS},'
assert src.count(old) == 1
pathlib.Path(os.environ.get("TMPDIR","/tmp"), "fb-wl-provider.py").write_text(src.replace(old, new))
PY
export FOREBRAIN_RUN_WORK="$W" FOREBRAIN_RUN_SESSION=fb-wl FOREBRAIN_RUN_PORT=8763
D=.claude/skills/run-forebrain/driver.sh
$D build                                   # 主树构建（先确认 peer 无 pkg/tui WIP）
ENABLE_SUBAGENT=1 $D serve subagent-steer 'sleep 25'
kill "$(cat "$W/provider.pid")"            # 换上有 send 旋钮的拷贝
FAKE_SUBAGENT_DISPATCH=send FAKE_SUBAGENT_STEER_FINISH=1 \
  python3 "${TMPDIR:-/tmp}/fb-wl-provider.py" subagent-steer 8763 'sleep 25' 0.6 \
  > "$W/provider.log" 2>&1 &
echo $! > "$W/provider.pid"
grep -q LISTENING "$W/provider.log" || sleep 1
tmux new-session -d -s fb-wl -x 120 -y 40 -c "$W/proj" \
  "FAKE_LLM_KEY=sk-local-fake FOREBRAIN_HOME=$W/home $W/forebrain 2>$W/tui.err; echo EXIT=\$?; exec bash"
# trust 页：出现 "Trust this folder?" 时 Down Enter；等 "/ commands"
$D submit 'dispatch the probe'
$D wait 'Worked for' 30                    # 主 turn 结束（send 不等待）
$D screen > "$W/before-main.txt"           # 证据：主视图 Worked for + roster running
$D key Down Down Enter                     # 进子代理视图（roster 导航）
$D wait 'viewing general-purpose' 10
$D screen > "$W/before-sub.txt"            # 证据（预期 bug）：子视图无 "Working ("
grep -c 'Working (' "$W/before-sub.txt"    # 预期 0
sleep 26; $D screen > "$W/before-sub-ended.txt"  # 子结束：预期出现 "Worked for"（worked-for 独立性确认）
```
**Verify（预期）**：`before-sub.txt` 无 `Working (` 而子代理仍在跑（`provider-log` 有子代理
REQUEST、screen 有 Running 工具卡）；`before-sub-ended.txt` 有 `Worked for`。
若主视图未出现 Worked for（send 未生效）→ 检查 provider-log 的 dispatch 工具名；
若子视图**有** Working 行 → **STOP**（链 A 与现场不符）。
取证文件保留，Step 7 修复后同配方复跑对照。

**1c 链 B 真机**：重跑 1b（`sleep 60`），主结束后子 running 期间：
`$D submit '/resume'` → 选另一会话（无则先 `/new` 建一个再 `/resume` 切回原会话）→
回原会话后 Down/Down/Enter 进子视图 → `$D screen`：预期**无** Working 行、roster 无子代理行。
子结束后其视图预期**无** Worked for。取证 `before-resume.txt`。

### Step 2：render.go 选择机制（fall-through + 重绘判定 + 跳绘）
按设计 A.2/A.3/A.5 改 `transientStatusTextLocked`、`syncTransientLocked`、
`finishTransientLocked`、`StartTimedTransientStatus`；删 `transientStatus` 字段与
`transientRenderLocked`；加 `transientSourceAgentWorking` 常量与优先级。
此步结束后旧 formatter 仍双视图渲染，行为暂时不变（agent 源尚未安装）。
**Verify**：`CGO_ENABLED=1 go build -tags fts5 ./...` exit 0；
`CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -run 'TestWorkingLineIsRenderedForTheViewOnScreen|TestTransient|TestMCP' -count=1` PASS
（notify_test.go:600-640 的 MCP 优先级用例必须仍绿）。

### Step 3：run.go formatter 拆分 + 协调器 + 接线
按设计 A.4/A.6 改 run.go；同步更新 10 处 `workingStatusFormatter` 测试调用点：
`run_test.go:3291,3308,3320,3339,4784,8044`（主视图用例，删第二参）、
`run_test.go:3363,7329,8119`（双子视图用例，拆成两个 formatter 分别断言）、
`reducer_test.go:4760`（删第二参）。行号仅定位用，以符号 grep 为准。
**Verify**：`CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -count=1` 全 PASS。

### Step 4：reducer.go + commands.go 收养（链 B）
按设计 B 加 `HasActiveAgentRun`、`adoptReplayedRunningAgents`、`afterReplay` 接线。
**Verify**：编译 exit 0；既有 replay 测试
`CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -run 'Replay|Resume|Rewind' -count=1` PASS。

### Step 5：回归测试（新增，写进既有测试文件）
见 Test plan。把 Step 1a 的复现测试**改写为新接线**（协调器 `agentWorkingLine.sync` +
拆分后的 formatter）落进 `run_test.go` 成为永久回归。
**Verify**：`CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -run 'TestSubagentWorkingLine|TestAgentWorking|TestReplayAdopts|TestWorkingLine' -count=1` 全 PASS。

### Step 6：全量门禁
`gofmt -l cmd pkg third_party plugins`（空）；`CGO_ENABLED=1 go vet -tags fts5 ./...`；
`CGO_ENABLED=1 go test -tags fts5 ./pkg/architecture -count=1`；
`FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/... ./cmd/forebrain -count=1`。
**Verify**：全绿。失败先诊断；若失败落在 peer WIP 文件 → `peer_send` 询问，不自行修。

### Step 7：真机验收（fake provider，必做）
`$D build` 重建（**漏 build 会跑旧二进制**）。复跑 Step 1b/1c 配方：
- 修复后子视图在主结束后**有** `Working (`，时钟是子代理自己的；主视图无行。
- 子结束后子视图 `Worked for` 仍在；`TransientStatusSourceLive` 语义：roster 空后无 ticker
  （screen 静止，无 5Hz 重绘迹象）。
- pause/resume：子视图 Ctrl+C 一次 → paused 帧、无 Working；输入消息 Enter → Working 恢复（主 idle）。
- /resume 往返：子视图 Working 恢复、roster 有行、子结束 Worked for 出现。
- 回归：普通 `reply` 模式 turn 的 Working/Worked for 不变；`hang`+Esc withdraw 不变；
  MCP 启动行仍压过 working（`subagent-limit` 或 mcp 配置场景可选）。
- 非 TTY：`forebrain gateway start </dev/null` 行为不变（pkg/tui 改动不应波及）。
逐屏 capture 存证；回填计划副本「验收记录」。清理：`$D stop`、tmux kill-session、scratch 目录。

### Step 8：收口
`git status --porcelain` 改动集合 == Scope（加 docs/plan 副本，gitignore 不显示）；
计划副本状态行改「已实施并真机验收通过（日期）」+ 验收记录表；不 commit。

## Test plan

新增用例（结构照 `chat_session_test.go:23667` / `run_test.go:3355` 模式）：
1. `run_test.go` `TestSubagentWorkingLineSurvivesMainRunEnd`：Step 1a 改写版；断言主结束后
   子视图仍有 Working、主视图无行、子 tally 未 ended。
2. `run_test.go` `TestSubagentWorkingLineResumesAfterPauseWhileMainIdle`：spawn→sync→pause
   （Reduce SubagentPausedMsg + sync）→ 无行；InputDelivered + sync → 有行。
3. `run_test.go` `TestAgentWorkingSourceStopsWhenLastSubagentEnds`：两子代理，逐个 ended+sync；
   最后一个结束后 `r.TransientStatusSourceLive(transientSourceAgentWorking)` false。
4. `run_test.go` `TestMainViewShowsNoWorkingLineWhileOnlySubagentsRun`：主 idle、agent 源 live，
   主视图 painted 无 `Working (`。
5. `chat_session_test.go` `TestTransientSelectionFallsThroughEmptyViewRender`：高优先级源对当前
   视图渲染 ""、低优先级源非空 → 画低优先级；视图切换立即生效（不等 tick）。
6. `commands_test.go` `TestReplayAdoptsRunningSubagentWindows`：照既有 replay 测试的 fake session
   模式，事件含 spawned(未 ended)+paused 另一子；replaySession 后 live tracker
   `SnapshotAgentRun` ok（paused 者 Paused=true）、roster 有行；再 Reduce(SubagentEndedMsg)
   产 Worked-for 帧；live 已知窗口的 id 不被覆盖（rewind 语义）。
7. 既有 `TestSubagentPauseIsDrawnInItsViewAndHidesItsWorkingLine`、
   `TestSubagentWorkingLineShowsItsOwnRetry`、`TestSubagentWorkingLineShowsFirstToken`、
   `TestSubagentViewIsClosedByItsOwnWorkedLine` 改调用点后语义不变，必须仍绿。

## Done criteria（机器可查）

- [ ] Step 1 复现修复前 FAIL（1a 含 `BUG REPRODUCED`；1b/1c 取证无行），修复后同配方 PASS。
- [ ] `CGO_ENABLED=1 go build -tags fts5 ./...` exit 0。
- [ ] `CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -count=1` 全 PASS，含 ≥6 个新用例。
- [ ] `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/... ./cmd/forebrain -count=1` 全 PASS。
- [ ] `CGO_ENABLED=1 go vet -tags fts5 ./...` exit 0；`gofmt -l cmd pkg third_party plugins` 空。
- [ ] `CGO_ENABLED=1 go test -tags fts5 ./pkg/architecture -count=1` PASS（文件数/同名测试门禁）。
- [ ] `grep -n "workingStatusFormatter(tracker.SnapshotActiveRun, tracker.SnapshotAgentRun)" pkg/tui/run.go` 无匹配（旧双参安装点已拆）。
- [ ] `git status --porcelain` 改动仅限 Scope 文件；无 commit（`git log -1` 仍为基线或 owner 后续提交）。
- [ ] 真机验收记录回填计划副本（场景→证据）。

## STOP conditions

- Step 1 任一复现**不失败**或失败原因与证据链不符（例如子视图本来就有行、或 precondition 失败）。
- 「Current state」摘录与现场内容不符（drift）。
- fall-through/跳绘改动使 MCP/connect/withdraw 既有测试失败且需改 out-of-scope 文件才能修。
- 发现 web 抽屉或 replay 已结束子代理的 Worked-for 也有同类症状（超出本计划两链，另行立项）。
- 同一验证失败两次仍不收敛；或需要新增生产文件/测试文件才能过架构门禁。
- peer 正在改 pkg/tui 任一 in-scope 文件（先 peer_send 协调）。

## Maintenance notes

- 瞬态源选择语义已变为「按优先级取第一个对当前视图有话说（非空）的源」。未来新增视图相关
  状态行时，formatter 对无关视图返回 "" 即可自动让位；不要再把某源的存活期绑到别的视图的
  生命周期上——本 bug 的根因正是这种绑定。
- `agentWorkingLine.sync` 的唯一判据是 `Tracker.HasActiveAgentRun`（!ended && !paused）。
  新增子代理生命周期消息（如未来某种 suspend）必须同时进 reducer 的 tracker 观察与
  processUINotification 的 sync case，否则行会漏装/漏删。
- replay 收养只补 live 缺失窗口；若未来 replay 开始映射更多生命周期事件，收养条件
  （startedAt 非零 && endedAt 零）仍是唯一入口，勿改覆盖语义。
- reviewer 重点：fall-through 后 MCP/LSP 优先级不变；跳绘不得吞掉视图切换即时性
  （closure 仍按视图求值）；主视图 Working 文案/时钟零变化。
- 遗留（不在本期）：roster 对 paused 子代理仍显示 "running" 状态（`SubagentPausedMsg`
  不更新 roster 行状态）——既有语义，与本 bug 无关，未改。

## 追加范围（owner 2026-11-17 评审期间报障）：跨视图"穿透"穷举修复（链 C）

owner 原话："error消息卡会从主代理视图'穿透'到子代理视图，这个bug也必须在本期一并修复。同类'穿透'问题必须在本期全部彻底修复"。
截图症状：子代理视图尾部出现**主 turn** 的 error 卡（token-plan.maas DNS 失败），位于该子代理自己的
`subagent failed` + `Worked for 42m49s …` 之后；主视图同一张卡在其 Worked-for 之前（主视图位置正确）。

### 根因 C1（源码证实）

`retainFrameLocked`（`pkg/tui/reducer.go:5978-6010`）对 `FrameError && AgentID==""` 的
"global error" 分支：先入主 VM，再镜像进**当前打开的**子代理视图：
```go
if r.activeView != "" {
    if vm := r.ensurePerAgentVM(r.activeView); !vm.endsOnError(f.Content) {
        vm.append(f)
    }
}
```
`endsOnError` 去重只挡"子视图已以同文案错误结尾"（usage-limit 同句场景）；子视图以
Worked-for 结尾时主错误照镜不误 → 截图症状。ownerless FrameError 的生产者包括
RunEndedMsg 的 run error（reducer.go:1428）、`PrintError`（render.go:880-887）、
MCP 连接错误卡（notify.go:437）——全部经此镜像穿透。
来源：初始导入 `4d4dea18` 即有；钉住测试
`TestViewportTerminalErrorMirrorsIntoActiveSubagentView`（chat_session_test.go:28559-28597）。
owner 新裁决（视图独立）推翻镜像。**保留**既有裁决「子代理**自己的**错误卡/重试卡与主视图
同一 UI/UX」（MEMORY.md:3627）——那是渲染管线一致，不是把主错误镜进子视图。

### 根因 C2（同类穷举命中，可达性证实）

`FinalizePendingTools`（render.go:941-956）在 `StreamResetMsg`（主 turn 取消/中断边界：
chat_session.go:2545、chat_turn.go:1074、chat_turn.go:1323，均主/foreground turn）时把
**所有** `perAgentVM` 的 pending 工具卡置 canceled：
```go
changed := r.finalizePendingToolsVM(&r.vm)
for _, vm := range r.perAgentVM {
    if r.finalizePendingToolsVM(vm) { changed = true }
}
```
`subagent_send` 的子代理在主 turn 结束后仍运行（reducer.go:1449-1451 注释："A dispatch's
agents outlive the turn"），主 Esc 会把它视图里正在跑的工具卡错误地标 Canceled。
子代理视图的 stale 卡只应由其自己的 `SubagentEndedMsg` → `FinalizeAgentPendingTools`
（run.go:407-409、render.go:962-975）收尾。

### 穷举审计结论（其余跨视图写入点均非穿透，勿改）

- `AgentID!=""` 路由（reducer.go:6021-6033）只入自己的 VM；`SubagentLifecycleCard`
  例外是刻意的会话账目（fanout/goal 卡）。
- `WithdrawDeadStreamBlocks`（reducer.go:5864-5877）按 agentID 取 VM，空则主 VM。
- `FinalizeAgentPendingTools`（render.go:962-975）只动署名 agent 的 VM。
- `retainCardOnceLocked`（reducer.go:5940-5954）去重 key 含 AgentID，不跨视图吞卡。
- `countNewMessageLocked`（reducer.go:5966-5973）只在可见 VM 长块时计数；镜像删除后自然诚实。
- plan_drawer.go:468/727、reducer.go:4508/5099/5700/6524 只读或绘制/点击命中，不写跨视图内容。
- `ViewportResetSession` 会话切换清屏是刻意行为。

### 修复设计 C

1. `reducer.go retainFrameLocked`：删除镜像块（6004-6008）及注释中的镜像语义；ownerless
   FrameError 只入主 VM（保留 insertBeforeLast 越过尾部 Worked-for 的**主视图**逻辑）。
   注释改写为视图独立语义：无主错误属于会话转录；子代理视图只收自己署名的帧。
2. `tty_session.go`：删除随之死掉的 `endsOnError`（1325-1346；唯一调用点即镜像）。
3. `render.go FinalizePendingTools`：只 finalize `&r.vm`，删除 perAgentVM 循环；注释说明
   子代理视图由其 own ended 事件收尾（FinalizeAgentPendingTools）。
4. 测试（既有文件内）：
   - 修订 `TestViewportTerminalErrorMirrorsIntoActiveSubagentView` → 更名
     `TestViewportTerminalErrorStaysOutOfSubagentView`：主 VM 有错误、子 VM 块数不变、
     子视口重绘不含该文案；保留 viewport mode 结构；注释注明契约修订理由
     （owner 2026-11-17 视图独立裁决）。
   - 新增 `TestOwnerlessErrorAfterSubagentWorkedLineStaysInPrimary`：子视图帧 + 子
     Worked-for 尾 → SetActiveView(子) → RenderFrame(ownerless FrameError) → 子 VM 不变；
     主 VM 的错误仍在尾部 Worked-for 之前（insertBeforeLast 语义不回归）。
   - 新增 `TestFinalizePendingToolsLeavesRunningSubagentCardsAlone`：主 VM pending 工具 +
     子 VM running 工具 → FinalizePendingTools → 主 canceled、子仍 running；再
     FinalizeAgentPendingTools(子) → 子 canceled。
5. 真机（scratch provider 拷贝加旋钮，**不改仓库技能文件**）：
   - C1：subagent-steer + `FAKE_SUBAGENT_DISPATCH=send` + 子 hang 变体；scratch 加
     `FAKE_MAIN_FAIL=1` 使主第二个请求返回 HTTP 500 → 主 error 卡；进子视图 capture →
     无该 error 卡（修复前有）。
   - C2：subagent-steer + send + 子 `sleep 25`；scratch 加 `FAKE_MAIN_HANG=1` 使主第二个
     请求 hang → 主 turn 在飞时 Esc → StreamReset → 子视图 sleep 工具卡仍 Running
     （修复前被 Canceled）。
   - 回归：子代理自己的 `subagent failed` 卡仍在其视图、同一 UI；主视图错误卡位置语义不变。

### 追加 Done criteria

- 修订/新增 3 用例 PASS；`grep -n "endsOnError" pkg/tui/*.go` 无匹配；
  `grep -n "ensurePerAgentVM(r.activeView)" pkg/tui/reducer.go` 无匹配。
- `FinalizePendingTools` 不再遍历 perAgentVM（diff 审查）。
- 真机 C1/C2 取证：修复前子视图有主 error 卡 / 子工具卡被 Canceled；修复后无。
- 原门禁全绿复跑（pkg/tui 全量 + 包级回归 + architecture + vet + gofmt）。

### 追加 STOP conditions

- 删除镜像后若某个**子代理自身**的错误不再出现在其视图（说明该错误帧未署名 AgentID）→
  STOP 报告：那是引擎署名缺陷，另行立项，不得在 TUI 补镜像掩盖。
- C2 收窄后若发现某路径子代理被主取消杀死却无 SubagentEndedMsg 到达 → STOP 报告。

### 追加环境注意

- 宿主磁盘曾 100%（ENOSPC 于 dsymutil）：go build/test 遇 ENOSPC 时用 `-p 2 -ldflags=-w`
  限并行并跳过 dwarf（语义不变，报告注明）；**不要再 `go clean -cache`**（peer 会话活跃）；
  评审者已清理孤儿 go-link/go-build 临时目录。

## 验收记录（执行者回填）

| 场景 | 证据 | 结果 |
|---|---|---|
| 链 A 修复前复现 | overlay 单测 FAIL（BUG REPRODUCED）+ before-sub.txt Working=0 | PASS |
| 链 B 修复前复现 | before-resume.txt roster/Working=0 | PASS |
| 链 A 修复后 | after-sub2.txt Working(13s)；评审者 rev3/rev4 独立复跑 | PASS |
| pause/resume（主 idle） | after-pause.txt / after-resume-input.txt；评审者 rev4-pause/resumed | PASS |
| 子结束 Worked-for | finish-sub-ended.txt；评审者单测覆盖 | PASS |
| 链 B 修复后 | after-resume2.txt / after-resume-ended.txt；评审者 rev4-resume-* 独立复跑 | PASS |
| 回归 hang/withdraw/reply/非TTY | regress-*.txt（执行者）；评审者单测+门禁 | PASS |
| C1 修复前/后 | prefix-c1b-sub-after.txt 有穿透 / c1b-sub-after.txt 0；评审者 rev2-c1-* 独立复跑 | PASS |
| C2 修复前/后 | prefix-c2-sub.txt Canceled / c2-sub.txt Running；评审者 rev3 独立复跑 | PASS |
| 子自身失败卡仍在子视图 | c1-subfail.txt（FAKE_SUB_FAIL） | PASS |
| D1 修复前/后 footer | d1-before-resumed-sub-footer.txt 仅 shortid / d1-after… 含 type+model；评审者 rev5-d1 独立复跑 | PASS |
| D2 修复前/后 roster | d2-before-main-roster-indistinct.txt / d2-after…shows-paused.txt / …paused-gone.txt；评审者 rev5-d2 独立复跑 | PASS |
| D 回归（链 A/C1） | regress-chainA-*.txt / regress-c1-*.txt（D-fixed 二进制） | PASS |
| E 修复前复现 | overlay golden FAIL（BUG REPRODUCED，旧从句在）；evidence/repro_e_test.go.txt | PASS |
| E 修复后文案 | golden PASS + 二进制字节对照 e3-binary-strings.txt；打印端不可达见 D14 | PASS |
| 落地 | owner 批准；rebase 无冲突；rebase 后门禁全绿；ff-merge dev=c17ebc1f；worktree/分支已删 | PASS |

## 追加范围 D（owner 2026-11-17：「遗留既有问题必须本期一并修复」）

Implementation Status 记录的两项遗留本期修掉：
D1 replay 恢复的子视图 footer 缺 agent type/model/effort；D2 roster 对 paused 子代理仍按 running 对待。

### 根因 D1（源码证实）

子视图 footer 左半 `subagentFooterLeftLocked`（reducer.go:4115-4122）读两张 map：
```go
agentType := r.subagentTypes[agentID]
model, effort := r.subagentModels[agentID].Model, r.subagentModels[agentID].ReasoningEffort
return joinFooterSegments(
	"viewing "+joinFooterSegments(agentType, shortAgentID(agentID)),
	"esc to return", model, effort,
)
```
两 map 唯一喂点 `NoteSubagentSpawned`（render.go:4152-4171）；live 在 run.go:345-352 喂
（`SubagentSpawnedMsg` → `NoteSubagentSpawned(msg.AgentID, msg.AgentType, ComposerFooter{
Model: subagentModelFooterLabel(msg.ModelProvider, msg.Model), ReasoningEffort: …})`）。
replay 路径 `replayTimelineWithWorkers`（commands.go:449+，归约接缝 commands.go:650
`result := reducer.Reduce(item.message)`）**从不喂** → resume 后 type/model/effort 全空 →
footer 只剩 `viewing <shortid> esc to return`。spawn 事件本身携带
ModelProvider/Model/ReasoningEffort（commands.go:1444 映射）——数据在，喂点缺。

### 根因 D2（源码证实）

`SubagentPausedMsg`（reducer.go:1655-1665）只做 `ObserveAgentPaused(true)` + paused 帧 +
`setCardTaskPaused`，不更新 roster 行 status；`SubagentInputDeliveredMsg`（1632-1650）resume
时也不恢复。roster 行 status 恒为 "running"（upsertSubagentRoster 默认）。既有词汇先例：
`updateSubagentStatus`（reducer.go:1953-1965）已用于 waiting_approval/cancelled/failed/running
（reducer.go:2222-2231）。
连锁：`agentRosterHasRunningSubagent`（render.go:5871-5878）只数 "running" → 若只改 status
不改 predicate，仅 paused 时 roster 整体消失（新缺陷）；故 predicate 须把 paused 计为
「仍活着的会话」。roster 行渲染 `formatAgentRosterLines`（render.go:5826-5867）目前完全不显示
status 词 → paused 与 running 行外观全同，读者看不出它在等自己。

### 修复设计 D

1. `commands.go replayTimelineWithWorkers` 归约接缝（~650）：`item.message` 为
   `SubagentSpawnedMsg` 时按 live 同字段同 helper 喂 `renderer.NoteSubagentSpawned`
   （`subagentModelFooterLabel`，run.go:1196）。注释：replay 与 live 喂同一份 per-agent footer 事实。
2. `reducer.go`：`SubagentPausedMsg` → `r.updateSubagentStatus(agentID, "paused")`；
   `SubagentInputDeliveredMsg` → `r.updateSubagentStatus(agentID, "running")`。
3. `render.go`：`agentRosterHasRunningSubagent` 更名 `agentRosterHasLiveSubagent`，
   计 running OR paused（注释：paused 是仍在等用户的会话，roster 必须继续列它）；
   三调用点同步：run.go:364、reducer.go:4142、render.go:5887（agentRosterOperable）。
4. `render.go formatAgentRosterLines`：`row.Status=="paused"` 的行在 detail 后追加一个
   空格连接的 `paused` 段（owner 文案裁决：空格连接、禁中点）；其余 status 不变。
5. waiting_approval 的既有可见性语义**不改**（不在 owner 本期清单，如实记录）。

### 测试 D

- `commands_test.go` `TestReplayFeedsSubagentFooterFacts`：spawn 事件带
  ModelProvider/Model/ReasoningEffort → `printSessionResumeContext` 后
  `subagentFooterLeftLocked(agent)` 同时含 type、model、effort。
- `reducer_test.go` `TestSubagentPauseUpdatesRosterStatus`：spawn→pause→roster 行
  Status=="paused"；`SubagentInputDeliveredMsg`→"running"。
- `render_test.go` `TestRosterListsPausedSubagentAsPaused`：paused-only snapshot →
  `agentRosterHasLiveSubagent` true、`agentRosterOperable` true（composer 收输入）、
  `formatAgentRosterLines` 含 "paused"；running 行不含该词。
- 真机：pause 后主视图 roster 行出现 paused；子视图发消息 resume 后消失；
  kill -9 + /resume → 子视图 footer 含 `viewing general-purpose` 与 model/effort 段。

### 追加 Done criteria / STOP

- 3 新用例 PASS；`grep -rn agentRosterHasRunningSubagent pkg/tui` 无匹配。
- D1/D2 真机取证（修复前 footer 缺段 / roster 行不可区分；修复后有）。
- 原门禁全绿复跑。
- STOP：roster paused 文案与既有文案 golden 冲突且需 scope 外改动；replay 喂
  NoteSubagentSpawned 后 live spawn 重投递错误覆盖 footer model（map 写幂等，不应发生）。
- Scope 不新增文件（render_test.go 已存在）。

## 追加范围 E（peer 93 转报 315 报告遗留；owner 常设裁决：预存缺陷不得留 follow-up）

缺陷：`pkg/tui/setup.go:3019` `formatStartupConfigError` 文案仍写
"…on the effective default entry — agents.definitions.main.default_model's target, **or the
first llm_providers entry** — in <path>"。315（exec-315-delete-llm-providers）删除
llm_providers 配置键及其 fallback（pkg/config load.go 等 -1100 行）；315 落地后该从句指向
退役概念 → 错误文案缺陷。已核：315 未碰 setup.go；315 的 setup_test.go 不钉该短语；
我方基线 setup_test.go 亦无钉住 → 改文案无既有测试冲突。

修复：删除 "or the first llm_providers entry" 从句，保留
"on the effective default entry — agents.definitions.main.default_model's target — in <path>"
（em-dash 括注为既有文案风格保留；owner 禁的是中点 ·）。`setup_test.go` 新增 golden：
`formatStartupConfigError` 输出含 "default_model's target" 且不含 "llm_providers"。
语义对准 315 落地后状态；落地顺序 315→本分支 保证终态正确（rebase 前基线仍有 fallback，
但分支不落地、无用户可见中间态）。

Scope 追加：`pkg/tui/setup.go`、`pkg/tui/setup_test.go`。
Done：golden PASS；`grep -rn "first llm_providers entry" pkg/tui` 无匹配；pkg/tui 全量 +
architecture + vet + gofmt + 包级回归复跑全绿；真机：不完整配置 scratch home 非 TTY 启动，
错误文案含新措辞、无 llm_providers。
STOP：313/319 若同改 setup.go 且与链 E 冲突（peer 协调未决）；copy golden 与 315
setup_test.go 冲突（已核不应发生）。

## Implementation Status

- **裁决：APPROVE（终）**。执行者四轮（链 A/B、链 C、链 D、链 E）均 STATUS: COMPLETE，无 STOP
  条件触发；评审者独立复跑全部门禁与关键真机场景后批准。**已落地**（见下「落地记录」）。
- **执行者**：opencode-go/deepseek-v4.1-flash（task subagent-a59a74fb-6b16-47fd-aa4c-5260f561baf6）。
- **worktree/分支**：`.worktrees/fix-subagent-working-line-decoupling` / 同名分支，基线 `aace268f`；
  保留不删（合并是 owner 的决定）。
- **commits**（落地前 worktree SHA → rebase 后 dev SHA）：
  `cb5719ed`→`fa413ed7` fix(tui): decouple subagent working line from the main turn（链 A/B）；
  `5c1bf86f`→`70aae058` fix(tui): keep ownerless errors and main-turn cancels out of subagent views（链 C）；
  `0fe11cec`→`94d5a03f` fix(tui): name resumed subagents in their footer and show paused roster rows（链 D）；
  `86f56ec0`→`c17ebc1f` fix(tui): drop the retired llm_providers clause from the startup config error（链 E）。
- **步骤状态**：Step 0–8 全 done；追加 C0–C4、D0–D4、E0–E4 全 done。无未做项。
- **评审者自跑验证（worktree HEAD=5c1bf86f）**：
  - `gofmt -l cmd pkg third_party plugins` → 空；`go vet -tags fts5 ./...` → clean；
  - `./pkg/architecture -count=1` → ok；`./pkg/tui -count=1` → ok（183.5s）；
  - `./pkg/... ./cmd/forebrain -count=1`（scratch home）→ **36 ok / 0 FAIL**；
  - `grep endsOnError pkg/tui/*.go`、`grep ensurePerAgentVM(r.activeView) pkg/tui/reducer.go`、
    旧双参 `workingStatusFormatter(tracker.SnapshotActiveRun, tracker.SnapshotAgentRun)` → 均 0 匹配；
  - 真机（自有 tmux/端口/新 home、fake provider、HEAD 二进制）：
    C1 子视图打开时主 turn 500 → 子视图 0 穿透且自身 Working 行在、主视图错误卡在其 Worked-for 之前
    （`evidence/rev2-c1-sub.txt` / `rev2-c1-main.txt`）；
    C2+链A 主 Esc 取消后子视图 `Running sleep 25`、Canceled=0、Working 行在（会话内工具输出取证）；
    pause/resume 主 idle → `Interrupted` 无 Working / 发消息后 Working 恢复（`evidence/rev4-pause.txt` / `rev4-resumed.txt`）；
    kill -9 后 /resume → roster 行恢复、子视图 Working 恢复（`evidence/rev4-resume-main.txt` / `rev4-resume-sub.txt`）。
  - 链 D（HEAD=0fe11cec）：gofmt 空、vet clean、architecture ok、pkg/tui ok(181.6s)、
    包级回归 36 ok/0 FAIL、`grep agentRosterHasRunningSubagent pkg/tui` 无匹配；
    真机 rev5：pause 后主视图 roster 行含 ` paused`（1 hit）、resume 消息后 0 hit
    （`evidence/rev5-d2-paused-roster.txt` / `rev5-d2-resumed-roster.txt`）；
    kill -9 + /resume 后子视图 footer `viewing general-purpose 56bb esc to return deepseek/deepseek-chat`
    （`evidence/rev5-d1-resumed-footer.txt`；修复前对照 `evidence/d1-before-resumed-sub-footer.txt`
    仅 `viewing ffc5 esc to return`）。
  - 链 E（HEAD=86f56ec0）：gofmt 空、vet clean、architecture ok、pkg/tui ok(182.0s)、
    包级回归 36 ok/0 FAIL、`grep "first llm_providers entry" pkg/tui` 无匹配；
    golden `TestStartupConfigErrorNamesTheEffectiveDefaultEntryOnly` 钉住新文案/缺字段列表/
    无 llm_providers/无中点；真机侧证据为二进制字节对照（`evidence/e3-binary-strings.txt`）
    + gateway/TUI 路径_capture（见 D14）。
  - **rebase 后（dev=ba68efdb 之上，HEAD=c17ebc1f）复跑**：gofmt 空、vet clean、
    architecture ok、pkg/tui ok(194.5s)、包级回归 **36 ok/0 FAIL**。rebase 无冲突。
- **偏差（均已披露，评审判为可接受）**：
  - D1 `cmds.afterReplay` 追加 `state.refreshAgentRoster(&reducer)`：链 B 收养的 roster 行在 reducer，
    而读者导航用的是 surface 快照，不刷新则恢复的子代理打不开；真机 before/after 证实必需。
  - D2 `pkg/tui/tty_session_test.go` 随 `endsOnError` 删除其钉住测试（scope 据此扩展；同名对应门禁满足）。
  - D3 第三个镜像钉住测试 `TestTurnErrorRepeatingSubagentFailureIsNotMirroredTwice` 的
    「异文案 global error 仍镜像」断言按 owner 新契约反向修订，其余断言保留。
  - D4 宿主磁盘 ENOSPC：build/test 用 `-p 1/2 -ldflags=-w`（语义不变）；执行者曾 `go clean -cache`
    一次（peer 下次构建变慢）；评审者清理孤儿 go-link/go-build 临时目录。
  - D5 修复前 1b「子结束→Worked for」真机取证未产（hang 变体子不结束）；由既有单测
    `TestSubagentViewIsClosedByItsOwnWorkedLine` + 修复后取证覆盖。
  - D6 执行者非 TTY gateway 检查曾短暂用真实 `~/.forebrain`（4s、无输出、即杀）——隔离规则偏差；
    评审者查 logs 目录未见该时段新增条目。
  - D7 链 B 真机复现需 `kill -9`（优雅退出会被 reaper settle 成 failed，无窗口可收养）。
  - D8 `NETWORK_RETRY_MAX` 首启导入 `fb_config`，复用 home 不生效 → 新 home；scratch 加
    `FAKE_MAIN_FAIL_AFTER` 旋钮（500 延迟，留出打开子视图的时间）。
  - D9 评审者 run1 单 Esc 未离子视图（roster 焦点先解除）→ 双 Esc 重跑取证。
  - D10 链 D 额外接线：`SubagentPausedMsg`/`SubagentInputDeliveredMsg` case 追加
    `state.refreshAgentRoster(&reducer)` + `rosterChanged=true`——surface roster 快照不刷新则
    paused 文案在真机不可见（单测过、真机不过的首次尝试已取证）；与 D1 同族、in scope。
  - D11 链 D 真机 footer 无 effort 段：fake provider 的 spawn 不携带 reasoning_effort；
    effort 由 `TestReplayFeedsSubagentFooterFacts` 确定性覆盖。footer 段连接为空格
    （owner 文案裁决 aace268f），评审 brief 里的中点写法仅为示意。
  - D12 执行者清理了 4 个孤儿 scratch provider 进程（端口 8766–8769，评审者早期 run 遗留、
    无 tmux 会话附着）；非活工作，清理正确。
  - D13 链 D 一次 `-overlay` 组合跑曾 `[build failed]`（磁盘 ~925MB）；释放后同命令 PASS；
    环境 ENOSPC，非代码。
  - D14 链 E 真机「打印端」取证不可达（执行者披露、评审核实接受）：headless gateway/serve 把
    `ErrStartupConfigIncomplete` 降级为 WARN（`gatewayAllowsModelToFollow`，owner 2026-11-07 /
    plan 268 裁决）；fresh home 的 TTY 路径进 onboarding wizard；非 TTY TUI 路径更早拒绝。
    完整诊断仅在「非首次运行但配置不完整」的 TTY 路径经 `interactive.go:120` 返回错误打印。
    故以二进制字节对照 + 确定性 golden 为比例适当的证据；不伪造打印端 capture。
- **限制**：执行者链 A/B/C 的 67 个真机取证 txt 及评审者 run1/run3 的 scratch 目录在评审期间被
  **外部清理**删除（非执行者/评审者所为）；关键场景已由评审者独立复跑，证据存于本计划目录
  `evidence/`（含链 D/E 执行者主动落盘系列与评审者 rev2/rev4/rev5 系列）。
  执行者报告的其余场景（sub-ended worked-for、reply/hang/withdraw 回归、gateway 非 TTY）未能从
  文件复核，以其报告 + 单测/门禁覆盖为准。
- **遗留**：原两项遗留（replay footer 缺 type/model、roster paused 显示 running）已于链 D
  修复；peer 93 转报的 setup.go 退役文案已于链 E 修复（93 已从 follow-up 划掉）。
  waiting_approval 的 roster 可见性语义仍为既有、不在 owner 清单、未改。
- **落地记录（owner 经 user_interaction 批准，2026-11-17）**：
  `git rebase dev`（ba68efdb，含 315 的 429987df 与 68 的 ba68efdb）无冲突 →
  rebase 后门禁全绿 → 主树 `git merge --ff-only` → **dev = c17ebc1f**（13 文件 +954/−192）→
  按常设规则 `git worktree remove` + `git branch -d`（worktree 与分支已删）→
  新尖已 peer_send 通知 forebrain-harness-93（313 rebase 基）与 forebrain-harness-68。
  93 预警的 render.go status 列冲突已核对不相交（我方 hunk 不碰
  panelLine/selectableColumns/columnRows；predicate 命名以 agentRosterHasLiveSubagent 为准）。