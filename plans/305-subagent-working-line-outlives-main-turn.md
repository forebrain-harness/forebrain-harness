# Plan 305: 子代理视图的 Working 行不再随主代理回合结束而消失（独立瞬态源 + 视图感知选择）

> **Executor instructions**：按步骤执行，每步跑完验证命令并对照预期再往下。遇到「STOP 条件」任一条就停下报告，不要自行发挥。完成后不要改 `plans/`（索引由派发方维护）。
>
> **Drift check（开工第一步）**：
> ```sh
> git rev-parse --short HEAD            # 基线 0a4be487（dev）；后代提交亦可，按内容核对
> grep -n 'StartTimedTransientStatus(transientSourceWorking' pkg/tui/run.go
> grep -n 'func workingStatusFormatter' -A 24 pkg/tui/run.go
> grep -n 'func (r \*Renderer) transientStatusTextLocked' -A 28 pkg/tui/render.go
> grep -n 'func (t \*Tracker) SnapshotAgentRun' -A 19 pkg/tui/reducer.go
> grep -n 'case SubagentSpawnedMsg:' -A 9 pkg/tui/run.go
> ```
> 期望：`run.go:4084` 是 `transientSourceWorking` 的**唯一**安装点（在 `runTurnWithDispatch` 内）；`workingStatusFormatter` 已按 view 分支（view 非空读 `agentSnapshot(view)`，`!ok || snap.Ended || snap.Paused` 返回 `""`）；`transientRenderLocked` 按 `transientSourcePriority` 取**第一个活着的源、不求值**，未知源走 map 随机序兜底；`SnapshotAgentRun` 对 `startedAt` 为零的 agent 返回 `ok=false`；`processUINotification` 的 switch 里有 `SubagentSpawnedMsg` / `SubagentEndedMsg` 两个 case（都只做 roster 刷新）。内容对不上即 STOP。
>
> **2026-11-17 reconcile 已在 `fcb241bc`（dev）实跑上述全部 grep 并逐段核对内容：全部成立，仅行号漂移**——安装点 4071→4084、`workingStatusFormatter` →run.go:4815、`transientStatusTextLocked` →render.go:1039（`transientRenderLocked` :1051）、`SnapshotAgentRun` →reducer.go:8737、两个 case →run.go:368/371（`SubagentEndedMsg` case 多了一段导航注释，行为仍只刷 roster）。基线 `0a4be487` 的后代提交（307/308/310/models 族）未触碰本计划契约。

## Status

- **Status**: **REJECTED — fixed independently (2026-11-17 reconcile on dev `42d4485d`)**.
  The fix this plan describes is already in the tree, landed by the working-line
  chain (`fa413ed7`/`94d5a03f`, dev `c17ebc1f`): there is a second source
  `transientSourceAgentWorking = "agent-working"` — "the subagents' own live
  line" (`pkg/tui/render.go:996`, ordered after `transientSourceWorking` at
  :1005-1006), installed **outside** `runTurnWithDispatch` at `run.go:4954`, and
  the main line's formatter is now view-aware (`workingStatusFormatter` →
  `run.go:4860-4864` returns `""` for a non-empty view). Nothing left to
  implement; do not re-add a third source. Kept for the record.
- **Priority**: P1（owner 2026-11-12 报障并裁决「必须定位根因并修复」）
- **Effort**: M（三个生产文件各一小段 + 四条新测试；无新生产文件、无迁移、无引擎改动）
- **Risk**: MED（改的是所有瞬态状态行共用的选择器；MCP/LSP/connect 行都经过它）
- **Depends on**: 无
- **Category**: bug fix
- **Planned at**: commit `0a4be487`，2026-11-12

## Why this matters

owner 截图：主视图已打出 `Worked for 8m 34s · …`（主回合结束），roster 里 `log-287` 仍在跑；切进该子代理视图，composer 上方**没有任何 Working 行**，只剩 `❯` 与 footer。一个正在干活的子代理，在属于它自己的视图里看起来像没在干活。

根因不是「行内容算错了」——内容早已解耦（见 Current state）——而是**行的生命周期被绑在主回合上**：Working 行唯一的安装点在 `runTurnWithDispatch`（`run.go:4071`），主回合一结束就被 `FinishTransientStatus` + stop 函数撤掉（`run.go:413/4100/4108`）。`subagent_send` 是 fire-and-forget（`pkg/run/subagent.go:3650-3704`，执行跑在 `context.Background()` 上、明确比派发它的回合活得久），所以「主回合已结束、子代理还在跑」是**设计内的正常状态**，而这个状态恰好是 Working 行的真空期：旧源已死，子代理自己的 `Worked for` 帧（`reducer.go:2265-2298`）要等它结束才出现。

修法：给子代理视图一个**独立的瞬态源**，生命周期跟着「是否还有未结束的子代理执行」走，与主回合无关；同时把瞬态源选择器改成**视图感知**（按优先级逐个求值、跳过对当前视图说空话的源），否则新源在主视图返回 `""` 会把 connect 等低优先级行挡成空白。两条 Working 行、两条 Worked for 行从此各管各的。

## Current state（执行者需要的事实，全部已逐行核实）

### 1. Working 行的安装与死亡（都在主回合里）

`pkg/tui/run.go:4071`（`runTurnWithDispatch` 内，唯一安装点）：

```go
stopStatus := renderer.StartTimedTransientStatus(transientSourceWorking, workingStatusFormatter(tracker.SnapshotActiveRun, tracker.SnapshotAgentRun))
```

`run.go:4092-4108`（回合退出时）：

```go
statusStopped := false
stopStatusOnce := func() {
	if !statusStopped {
		statusStopped = true
		stopStatus("")
	}
}
defer func() {
	renderer.FinishTransientStatus(transientSourceWorking)
	…
}()
defer stopStatusOnce()
```

`run.go:407-416`（主回合的 `WorkedStatus` 到达时，先撤 Working 再落 Worked for 帧）：

```go
if ev.WorkedStatus != "" {
	turnWorkedStatus = ev.WorkedStatus
	renderer.FinishTransientStatus(transientSourceWorking)
	renderer.RenderFrame(Frame{Kind: FrameStatus, Title: ev.WorkedStatus, Final: true})
	…
```

### 2. 行内容早已视图解耦（不要重做这部分）

`run.go:4802-4852` `workingStatusFormatter(counterSnapshot, agentSnapshot)`：`view != ""` 时读 `agentSnapshot(view)`（即 `Tracker.SnapshotAgentRun`），`!ok || snap.Ended || snap.Paused` 返回 `""`；否则拼 `transientSpinnerFrame(tick) + " Working (" + formatWorkingElapsed(snap.Elapsed) + ")"` + counters + first-token，retry 活跃时改走 `transportRetryStatusLine`（`:4812-4814`）。主视图分支才用 `counterSnapshot()` 与 `esc to interrupt`。

子代理的 Worked for 也早已独立：`reducer.go:2265-2298` `subagentWorkedFrames(agentID)` 读同一个 `SnapshotAgentRun`，帧带 `AgentID`，由 `SubagentEndedMsg` 触发（`reducer.go:1648-1693`）。**这两处都不需要改语义。**

### 3. 瞬态源机制（`pkg/tui/render.go`）

- `:989-1002` 源常量与优先级：

```go
const (
	transientSourceMCP     = "mcp"
	transientSourceLSP     = "lsp"
	transientSourceWorking = "working"
	transientSourceConnect = "connect"
)

var transientSourcePriority = []string{
	transientSourceMCP,
	transientSourceLSP,
	transientSourceWorking,
}
```

（`connect` 不在优先级表里，走未知源兜底。）

- `:1034-1060` 选择器**先选源、后求值**——被选中的源对当前视图返回 `""` 时，整行空白，不会落到下一个源：

```go
func (r *Renderer) transientStatusTextLocked() string {
	if !r.transientActive {
		return ""
	}
	if render := r.transientRenderLocked(); render != nil {
		return render(r.activeView)
	}
	return ""
}

func (r *Renderer) transientRenderLocked() func(view string) string {
	for _, source := range transientSourcePriority {
		if render, ok := r.transientSources[source]; ok && render != nil {
			return render
		}
	}
	for _, render := range r.transientSources { // map 随机序
		if render != nil {
			return render
		}
	}
	return nil
}
```

- `:1064-1069` `syncTransientLocked` 用 `transientRenderLocked` 镜像「有没有活源」到 `r.transientStatus`/`r.transientActive`；`:1158-1175` `finishTransientLocked` 只在活/死翻转时重绘。真正的 painted 文本来自 `reducer.go:3840` `statusText := strings.TrimSpace(r.transientStatusTextLocked())`（`buildComposerBlock`，只读 composer 同样画它）。
- `:1111-1149` `StartTimedTransientStatus`：**200ms ticker 每 tick 重新 install 一次源**；`FinishTransientStatus` 只删 map 条目，**下一 tick 就复活**——唯一真正的终止是调用它返回的 stop 函数（stop channel + `<-done`）。这是本计划最容易踩的坑：新源必须持有并调用 stop 函数，不能只 Finish。
- `:1085-1093` `FinishAllTransientStatus` 清掉全部源（同样不杀 ticker goroutine）。
- `render.go` 目前**没有** import `sort`/`slices`（`:4-24`）；仓库 Go 1.26.9，用 `slices.Sort`。

### 4. Tracker 与生命周期钩子

- `reducer.go:8498-8516` `SnapshotAgentRun`：`startedAt` 为零 → `ok=false`；`endedAt` 非零 → `Elapsed` 冻结、`Ended=true`；`Paused: run.paused && !ended`。`agentRuns` 是会话级 map（`Reset()` 清空，`reducer.go:8276`）。**Tracker 今天没有「是否还有未结束子代理」的查询**——要新加。
- `run.go:244` `tracker := NewTracker()`，与 `:281` 的 `processUINotification` 闭包同作用域；所有 UI 通知在主循环单 goroutine 串行处理（`:255-263` 注释），`streamState` 无锁、只主循环访问。
- `run.go:337` 先 `ev := reducer.Reduce(m)`（`SubagentSpawnedMsg` → `ObserveAgent`，`SubagentEndedMsg` → `ObserveAgentEnded`，`reducer.go:1601-1693`），**之后** `:356-374` 的 switch 才跑：

```go
case SubagentSpawnedMsg:
	state.refreshAgentRoster(&reducer)
	rosterChanged = true
case SubagentEndedMsg:
	state.refreshAgentRoster(&reducer)
	rosterChanged = true
```

- 会话切换 `run.go:2048-2061`（`switchStreamSession`，签名 `:1960` 带 `state/renderer/tracker`）：`tracker.Reset()` + `renderer.FinishAllTransientStatus()`。`resetStreamTranscript`（`:1945-1953`，/rewind 等回放清屏）只 `FinishAllTransientStatus`，不动 tracker。
- `streamState`（`run.go:1740`）已有函数字段先例：`resumeRawRead func()`（`:1789`）。

### 5. 引擎侧事实（回答 owner 三问，**不在本计划改动范围**）

- `subagent_send` fire-and-forget：`pkg/run/subagent.go:3650-3704` → `spawnAsyncSubagent`（`:2243-2294`），执行上下文来自 `asyncSubagentContext`（`:2215-2241`，`context.Background()`，注释明写 outlives the turn）。阻塞式 `subagent_run` 会等（`:390-483`）。
- 子代理结束：`finishSubagent`（`:4136-4166`）落历史 + 发 `subagent_ended`（`notifySubagentEndedDirect`，`:2368-2380`，带 Output），**没有**自动唤醒主代理；主代理用 `subagent_wait`/`subagent_status`/`subagent_continue` 收结果。
- 唯一唤醒路径是 peer 通道：子代理 `peer_send` → `deliverPrimary`（`pkg/process/peer_inbound.go:754-790`，idle → `surface.Wake`）→ TUI `PeerWakeMsg` → 主循环 `executePeerTurn`（`run.go:772-774`）。

### 6. 仓库约定与守卫

- 测试文件命名：`X_test.go` 必须 beside `X.go`，文件名 ≤2 个下划线（`pkg/architecture/cache_test.go:1445/1497`）；每包生产文件 ≤34（`:1531`，`pkg/tui` 现 31）。**本计划不新建任何文件**：生产改动进 `run.go`/`render.go`/`reducer.go`，测试进既有 `run_test.go`/`notify_test.go`。
- 注释风格：解释 WHY、用完整句子，参考 `run.go:4790-4801` 与 `render.go:1071-1073`。
- 既有测试样板：
  - `run_test.go:3353` `TestWorkingStatusFormatterReportsTheSubagentWhoseViewIsOpen`（formatter 断言形状）
  - `run_test.go:3387` `TestTrackerFreezesASubagentsClockWhenItEnds`（tracker 测试形状）
  - `run_test.go:3899` `TestSwitchStreamSessionResetsTransientSessionState`（`switchStreamSession` + `fakeSession` 形状）
  - `run_test.go:5383` `TestMCPStatusLineKeepsItsOwnClock`（`require.Eventually` 等 ticker goroutine）
  - `notify_test.go:592` `TestTransientStatusSourcesDoNotClearEachOther`（`painted()` = `stripANSI(strings.Join(r.buildComposerBlock(state, 120).lines, "\n"))`，`NewRenderer(nil, nil)`）
  - `notify_test.go:313` `require.Never`（断言「不发生」的形状）
  - `renderer.SetActiveView("agent-id")` 切换视图（`run_test.go:2840` 等多处用例）

## Commands you will need

本机 `go` 不在默认 PATH，先 `export PATH=/usr/local/go/bin:$PATH`。

| Purpose | Command | Expected on success |
|---------|---------|---------------------|
| 定向测试 | `CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -run '<Filter>' -count=1` | ok |
| 全包测试 | `CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -count=1` | ok |
| 竞态 | `CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -race -count=1` | ok，0 DATA RACE |
| 构建 | `CGO_ENABLED=1 go build -tags fts5 ./...` | exit 0 |
| Vet | `go vet -tags fts5 ./...` | exit 0 |
| 格式 | `gofmt -l pkg cmd` | 输出为空 |
| 架构守卫 | `CGO_ENABLED=1 go test -tags fts5 ./pkg/architecture -count=1` | ok |

基线已验证：`CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -run 'TestWorkingStatusFormatterReportsTheSubagentWhoseViewIsOpen|TestTrackerFreezesASubagentsClockWhenItEnds' -count=1` → ok（0.768s）。链接期 `ld: warning: … built for newer 'macOS' version` 是本机既有噪声，忽略。

## Scope

**In scope**（只许改这些）：
- `pkg/tui/run.go`（抽共享行函数、新 formatter、`streamState` 新字段、spawn/end 钩子、会话切换停源）
- `pkg/tui/render.go`（新源常量与优先级、视图感知选择器）
- `pkg/tui/reducer.go`（`Tracker.HasLiveAgentRuns`）
- `pkg/tui/run_test.go`、`pkg/tui/notify_test.go`（新测试）

**Out of scope**（看着相关也不许动）：
- `pkg/run`、`pkg/process`、`pkg/turn`、`pkg/event`：引擎生命周期是设计内行为（Current state §5），本计划零引擎改动；**不要**顺手加「子代理结束自动唤醒主代理」。
- `workingStatusFormatter` 主视图分支与 `subagentWorkedFrames` 的语义：已经是对的。
- `runTurnWithDispatch` 里主回合 working 源的安装/停止逻辑：保持原样，两源并存。
- 会话切换/恢复后 tracker 不重播种的缺口（见「Maintenance notes」）：本计划明确延后，不要在本计划里修。
- 不新建任何生产文件或测试文件（架构守卫 §6）。

## Steps

### Step 1：`Tracker.HasLiveAgentRuns`

在 `reducer.go` 的 `SnapshotAgentRun`（`:8498`）附近新增：

```go
// HasLiveAgentRuns reports whether any observed subagent execution has not
// ended. A paused execution counts as live: it resumes without a new spawn
// event, and the status source must still be there when it does.
func (t *Tracker) HasLiveAgentRuns() bool {
	if t == nil {
		return false
	}
	t.mu.RLock()
	defer t.mu.RUnlock()
	for _, run := range t.agentRuns {
		if !run.startedAt.IsZero() && run.endedAt.IsZero() {
			return true
		}
	}
	return false
}
```

新测试进 `run_test.go`（tracker 测试都在这文件，样板 `:3387`）：`TestTrackerHasLiveAgentRunsCountsPausedUntilEnded` —— ① 空 tracker false；② `ObserveAgent("a","",time.Now())` 后 true；③ `ObserveAgentPaused("a",true)` 仍 true；④ `ObserveAgentEnded("a",time.Now())` 后 false；⑤ 两个 agent 一死一活仍 true；⑥ 只被 `ObserveToolStep` 碰过、从未 `ObserveAgent` 的 id 不算活（`startedAt` 为零）。

**Verify**：`CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -run 'TestTrackerHasLiveAgentRuns' -count=1 -v` → PASS。

### Step 2：共享子代理行函数 + 新 formatter（只加不改语义）

在 `run.go` `workingStatusFormatter`（`:4802`）旁：

1. 把 `workingStatusFormatter` 的 view 分支（`:4808-4824`，含 retry 分支）抽成 `subagentWorkingLine(snap AgentRunSnapshot, tick int) string`，原分支改为取 snap、判 `!ok || snap.Ended || snap.Paused` 后调用它。**逐字节保留现有输出**（spinner、`Working (…)`、counters、first-token、retry 行）。
2. 新增：

```go
// subagentWorkingStatusFormatter is the working line's second, independent
// source: it speaks only inside a subagent's view, and only while that
// agent's execution is live. The conversation's turn installs its own working
// source and takes it down when the turn ends; this one answers to the
// subagents' lifecycle instead, so a fire-and-forget agent still shows its
// own clock after the turn that dispatched it has closed.
func subagentWorkingStatusFormatter(agentSnapshot func(string) (AgentRunSnapshot, bool)) transientStatusFormatter {
	return func(_ time.Duration, tick int, view string) string {
		if view = strings.TrimSpace(view); view == "" || agentSnapshot == nil {
			return ""
		}
		snap, ok := agentSnapshot(view)
		if !ok || snap.Ended || snap.Paused {
			return ""
		}
		return subagentWorkingLine(snap, tick)
	}
}
```

新测试进 `run_test.go`（样板 `:3353`）：`TestSubagentWorkingStatusFormatterOnlySpeaksInsideSubagentViews` —— 主视图（`view=""`）恒 `""`（即使有活 agent）；活 agent 视图出 `Working (` + 该 agent 自己的 elapsed；ended / paused / 未知 agent 均 `""`；与 `workingStatusFormatter` 的 view 分支对同一 snap 输出**逐字相等**。

**Verify**：`CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -run 'TestSubagentWorkingStatusFormatter|TestWorkingStatusFormatter' -count=1` → ok（既有 formatter 测试一条不许红、断言不许放宽）。

### Step 3：视图感知的瞬态源选择器

`render.go`：

1. import 加 `"slices"`。
2. 新增 `transientCandidatesLocked() []func(view string) string`：按 `transientSourcePriority` 序收集活源，再把不在优先级表里的源**按名字排序**（`slices.Sort`）追加——取代今天的 map 随机序兜底。注释写明：候选按显示优先级排列，求值方跳过对当前视图说空话的源。
3. `transientStatusTextLocked` 改为遍历候选、逐个 `render(r.activeView)`，返回第一个 `strings.TrimSpace` 后非空的文本；全空返回 `""`。
4. `transientRenderLocked` 改为返回候选首个（无候选返回 nil）——`syncTransientLocked` 与 `finishTransientLocked` 的「活/死」语义不变，**不要动它们**。

新测试进 `notify_test.go`（样板 `:592` 的 `painted()` 形状）：`TestTransientStatusPickerSkipsSourcesThatSayNothingForTheView` ——
① 装一个对 `view=="sub-1"` 返回 `""`、主视图返回文本的源（模拟 working）+ 一个固定文本的未知源（模拟 connect）：主视图画 working 文本；`SetActiveView("sub-1")` 后画 connect 文本（旧代码这里空白——这是本步的核心断言）；
② 高优先级源对该视图有文本时仍赢过低优先级；
③ 两个未知源同时活着时按名字序稳定取胜（多画几次不变）；
④ `FinishTransientStatus` 掉高优先级源后低优先级接上。

**Verify**：`CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -run 'TestTransientStatus' -count=1` → ok，**含既有 `TestTransientStatusSourcesDoNotClearEachOther` 必须仍 PASS 且一字未改**。

### Step 4：独立子代理源 + 生命周期接线

1. `render.go:989-1002`：常量块加 `transientSourceSubagent = "subagent"`（注释：子代理视图自己的 working 行，生命周期跟子代理执行走、与主回合无关）；`transientSourcePriority` 末尾追加它（working 之后）。
2. `run.go` `streamState`（`:1740`，`resumeRawRead` 附近）加字段：

```go
// subagentStatusStop ends the subagent views' own working line. Main-loop
// only, like the rest of streamState; nil while no subagent execution is
// live. The stop call — not FinishTransientStatus — is what terminates the
// source's ticker, which would otherwise reinstall the line every 200ms.
subagentStatusStop func(string)
```

3. `processUINotification` 的 switch（`:356-374`）：`SubagentSpawnedMsg` case 里，roster 刷新后，若 `state.subagentStatusStop == nil` 则安装：

```go
state.subagentStatusStop = renderer.StartTimedTransientStatus(transientSourceSubagent, subagentWorkingStatusFormatter(tracker.SnapshotAgentRun))
```

`SubagentEndedMsg` case 里（此时 `reducer.Reduce` 已跑过 `ObserveAgentEnded`），若 `state.subagentStatusStop != nil && !tracker.HasLiveAgentRuns()` 则 `state.subagentStatusStop("")` 并置 nil。
4. `switchStreamSession`（`:2048` `tracker.Reset()` 之前）：若 `state.subagentStatusStop != nil` 则停掉并置 nil——会话切换后旧会话的子代理 tally 已清，ticker 不许再复活。
5. **`resetStreamTranscript` 不动**：回放清屏后 ticker 会在 200ms 内重新装回，与主回合 working 行今天的行为一致（主回合还在跑时 /rewind  likewise 靠复活恢复行）。

新测试进 `run_test.go`：
- `TestSubagentWorkingLineOutlivesTheMainTurn`（核心，样板 `:5383` + `notify_test.go:592`）：`NewRenderer(nil,nil)` + tracker；`ObserveAgent("sub-1","",time.Now().Add(-57*time.Second))`；按 `runTurnWithDispatch` 的形状装 working 源（`workingStatusFormatter(tracker.SnapshotActiveRun, tracker.SnapshotAgentRun)`）并装新源；`SetActiveView("sub-1")`，`require.Eventually` 等到 painted 含 `Working (`；然后模拟主回合结束——`FinishTransientStatus(transientSourceWorking)` + 调 working 的 stop 函数——painted **仍**含 `Working (57s` 量级的行（这是修复前必红的断言）；切回主视图（`SetActiveView("")`）该行消失；`ObserveAgentEnded` + 按 Step 4 的停止逻辑停新源后，`require.Never` 断言 `TransientStatusSourceLive(transientSourceSubagent)` 不再成立（300ms 窗口，证明 ticker 没复活）。
- `TestSwitchStreamSessionStopsTheSubagentStatusSource`（样板 `:3899`）：给 `state.subagentStatusStop` 装一个真源（tracker 有活 agent），跑 `switchStreamSession`，断言字段为 nil、`require.Never` 断言源不再 live（300ms 窗口）。

**Verify**：`CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -run 'TestSubagentWorkingLineOutlivesTheMainTurn|TestSwitchStreamSessionStopsTheSubagentStatusSource' -count=1 -v` → PASS。**红绿演示（写进报告）**：临时注释掉 spawn 钩子的安装行，核心测试必须红；恢复后复绿。

### Step 5：回归

`CGO_ENABLED=1 go build -tags fts5 ./...`、`go vet -tags fts5 ./...`、`gofmt -l pkg cmd` 空、`CGO_ENABLED=1 go test -tags fts5 ./pkg/tui ./pkg/architecture -count=1` → ok，再 `CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -race -count=1` → ok 0 DATA RACE。

## Test plan（汇总）

| 文件 | 新测试 | 覆盖 |
|------|--------|------|
| `run_test.go` | `TestTrackerHasLiveAgentRunsCountsPausedUntilEnded` | 空/活/暂停/结束/多 agent/未 ObserveAgent |
| `run_test.go` | `TestSubagentWorkingStatusFormatterOnlySpeaksInsideSubagentViews` | 主视图恒空；活/死/暂停/未知；与旧 view 分支逐字相等 |
| `notify_test.go` | `TestTransientStatusPickerSkipsSourcesThatSayNothingForTheView` | 空话源被跳过、优先级不变、未知源名字序稳定 |
| `run_test.go` | `TestSubagentWorkingLineOutlivesTheMainTurn` | 主回合死后子代理视图仍有行；主视图无行；停源后不复活 |
| `run_test.go` | `TestSwitchStreamSessionStopsTheSubagentStatusSource` | 切换会话停源、字段清 nil、不复活 |

既有测试一条不许放宽：`TestWorkingStatusFormatter*`（`:3289-3417`）、`TestTransientStatusSourcesDoNotClearEachOther`（`notify_test.go:592`）、`TestMCPStatusLineKeepsItsOwnClock`（`:5383`）、`TestSwitchStreamSessionResetsTransientSessionState`（`:3899`）、`TestEscapePrecedencesStayAheadOfTheMCPStartupSkip`（`:5304`）。

## Done criteria（全部满足）

- [ ] Drift check 通过；五个新测试存在且 PASS
- [ ] 红绿演示记录在报告（去掉安装行 → 核心测试红；恢复 → 绿）
- [ ] `CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -count=1` ok；`-race` ok 0 DATA RACE
- [ ] `CGO_ENABLED=1 go build -tags fts5 ./...`、`go vet -tags fts5 ./...` exit 0；`gofmt -l pkg cmd` 空
- [ ] `CGO_ENABLED=1 go test -tags fts5 ./pkg/architecture -count=1` ok（无新文件、命名守卫不红）
- [ ] `git status` 改动只落在 Scope 的五个文件；既有测试断言零放宽
- [ ] 引擎包（`pkg/run`/`pkg/process`/`pkg/turn`/`pkg/event`）零改动

## STOP 条件（停下报告，不要自行发挥）

- Drift check 任一处与现状不符（代码已漂移）。
- Step 3 打红了任何既有瞬态源测试且无法在**不改断言**的前提下修复——选择器语义冲突比本计划预想的深。
- 核心测试在「去掉安装行」后仍然绿——说明测试没钉住修复点，先修测试再往下。
- `-race` 报 `subagentStatusStop` 或 tracker 新方法的竞态——说明有主循环之外的访问点，报告该访问点，不要自行加锁绕。
- 发现修复必须动引擎包、或必须新建生产/测试文件才能成立。
- 真机验证（若派发方要求）：主回合结束后进入仍在跑的子代理视图，Working 行必须出现且秒数是该子代理自己的时钟；子代理结束后该行消失、其 `Worked for` 帧正常。

## Maintenance notes

- **已知缺口（本计划明确延后，勿顺手修）**：会话切换 `tracker.Reset()`（`run.go:2049`）与回放用一次性 tracker（`commands.go:158`）都不重播种 `agentRuns`，所以「切走再切回一个仍有子代理在跑的会话」或「resume 时已在跑的子代理」在下一个生命周期事件前仍没有 Working 行（`SnapshotAgentRun` 对 `startedAt` 为零返回 ok=false）。修它需要从引擎 roster 重播种 tracker，是独立一件事。
- **行为变化（评审时确认这是想要的）**：视图感知选择器生效后，已结束子代理的视图不再强制空白——若此时 MCP/LSP/connect 行活着，它们会显示出来（旧代码里 working 源对已结束视图返回 `""` 会挡住低优先级源）。主回合运行期间子代理视图的显示不变（working 源优先级更高、内容相同）。
- 新源的停止只挂在 `SubagentEndedMsg` 与会话切换上。若未来新增「子代理执行结束但不发 `SubagentEndedMsg`」的路径，必须同步补停源点，否则 ticker 永转。
- 进程退出时若仍有子代理在跑，新源 goroutine 随进程结束——与今天 MCP/working 源的行为一致，无需额外清理。
- 评审重点：Step 3 的选择器重写是否保持 `syncTransientLocked`/`finishTransientLocked` 的活/死语义；Step 4 的 stop 函数是否在所有路径都被调用（Finish 不杀 ticker）。